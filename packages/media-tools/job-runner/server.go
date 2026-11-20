package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// server serves the job contract of docs/design/render-runtime.md (section 3), the same one the gateway Worker serves:
//
//	PUT    /jobs/{id}   start the job in the body (a Manifest); idempotent: the same id and manifest answer the job's status
//	GET    /jobs/{id}   the job's Status
//	DELETE /jobs/{id}   stop the job if it runs and forget it
//	GET    /healthz     liveness, without auth
//
// Every /jobs route needs `Authorization: Bearer <token>`. The runner runs one job at a time (one sandbox per job): a PUT of a
// new id while another job runs answers 503 with Retry-After. A finished job's status is kept for keep, then forgotten.
type server struct {
	r     *Runner
	token []byte
	// allowedHosts are the host:port pairs inputs and outputs may use; empty allows any (egress is then the platform's job).
	allowedHosts []string
	keep         time.Duration
	base         context.Context

	mu      sync.Mutex
	jobs    map[string]*job
	current *job
}

func newServer(base context.Context, r *Runner, token string, allowedHosts []string) *server {
	return &server{r: r, token: []byte(token), allowedHosts: allowedHosts, keep: time.Hour, base: base, jobs: map[string]*job{}}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("PUT /jobs/{id}", s.auth(s.put))
	mux.HandleFunc("GET /jobs/{id}", s.auth(s.get))
	mux.HandleFunc("DELETE /jobs/{id}", s.auth(s.delete))
	return mux
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), s.token) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !validID(req.PathValue("id")) {
			writeError(w, http.StatusBadRequest, "invalid job id")
			return
		}
		next(w, req)
	}
}

func (s *server) put(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	data, err := io.ReadAll(io.LimitReader(req.Body, maxManifestBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read the manifest: "+err.Error())
		return
	}
	if len(data) > maxManifestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "the manifest is too large")
		return
	}
	m, err := parseManifest(data)
	if err == nil {
		err = checkHosts(s.allowedHosts, m)
	}
	if err == nil {
		err = s.r.checkCapacity(m)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	s.purgeLocked(time.Now())
	if j, ok := s.jobs[id]; ok {
		s.mu.Unlock()
		if j.hash != m.hash() {
			writeError(w, http.StatusConflict, "job "+id+" exists with a different manifest")
			return
		}
		writeJSON(w, http.StatusOK, j.status())
		return
	}
	if s.current != nil && !s.current.terminal() {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "busy: this runner runs one job at a time")
		return
	}
	j := newJob(s.base, id, m)
	s.jobs[id] = j
	s.current = j
	s.mu.Unlock()

	go s.r.execute(j)
	writeJSON(w, http.StatusCreated, j.status())
}

func (s *server) get(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	s.purgeLocked(time.Now())
	j, ok := s.jobs[req.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, j.status())
}

func (s *server) delete(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	j.cancel()
	// The job's processes are killed and its files removed before the answer, so the next job starts clean.
	select {
	case <-j.done:
	case <-time.After(30 * time.Second):
		writeError(w, http.StatusServiceUnavailable, "the job did not stop in time")
		return
	}
	s.mu.Lock()
	if s.jobs[id] == j {
		delete(s.jobs, id)
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// purgeLocked forgets the jobs that finished more than keep ago.
func (s *server) purgeLocked(now time.Time) {
	for id, j := range s.jobs {
		if !j.terminal() {
			continue
		}
		j.mu.Lock()
		old := now.Sub(j.finished) > s.keep
		j.mu.Unlock()
		if old {
			delete(s.jobs, id)
		}
	}
}

// checkHosts refuses a manifest whose URLs point anywhere but the allowed hosts, before anything is fetched.
func checkHosts(allowed []string, m Manifest) error {
	if len(allowed) == 0 {
		return nil
	}
	urls := make([]string, 0, len(m.Inputs)+len(m.Outputs))
	for _, in := range m.Inputs {
		urls = append(urls, in.URL)
	}
	for _, o := range m.Outputs {
		urls = append(urls, o.URL)
	}
	for _, raw := range urls {
		u, _ := url.Parse(raw) // validated already
		if hp := hostPort(u); !slices.Contains(allowed, hp) {
			return fmt.Errorf("manifest: the host %s is not one this runner may reach", hp)
		}
	}
	return nil
}

// hostPort is the URL's host:port, with the scheme's default port when it names none.
func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return u.Host + ":443"
	}
	return u.Host + ":80"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
