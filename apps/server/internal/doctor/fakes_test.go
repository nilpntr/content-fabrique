package doctor

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/nilpntr/content-fabrique/internal/envfile"
)

// loadExample reads the repository's real .env.example, so the tests follow the tags the way doctor does.
func loadExample(t *testing.T) []envfile.Var {
	t.Helper()
	f, err := os.Open("../../../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	vars, err := envfile.ParseExample(f)
	if err != nil {
		t.Fatal(err)
	}
	return vars
}

var routingFS = os.DirFS("../../config/routing")

// goodVars returns the .env.example defaults with the generated secrets filled in and the [YOU-...] values empty.
func goodVars(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, v := range loadExample(t) {
		out[v.Name] = v.Default
	}
	out["SECRETS_MASTER_KEY"] = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // 32 bytes
	out["SANDBOX_GATEWAY_TOKEN"] = strings.Repeat("ab", 32)
	out["RIVER_UI_PASSWORD"] = strings.Repeat("cd", 16)
	return out
}

// find returns the result with the exact name.
func find(t *testing.T, rep Report, name string) Result {
	t.Helper()
	for _, r := range rep.Results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no result named %q", name)
	return Result{}
}

func wantStatus(t *testing.T, rep Report, name string, want Status) Result {
	t.Helper()
	r := find(t, rep, name)
	if r.Status != want {
		t.Errorf("%s = %s (%s), want %s", name, r.Status, r.Reason, want)
	}
	return r
}

// fakeS3 is a path-style object store: PUT, GET and DELETE on /bucket/key.
type fakeS3 struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	left    func() int
}

func newFakeS3(t *testing.T, bucket string) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/"+bucket+"/doctor/") || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			f.objects[r.URL.Path] = data
			f.puts++
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := f.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(f.objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(f.Close)
	f.left = func() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.objects) }
	return f
}

// fakePG speaks just enough of the Postgres wire protocol for pgx to connect, run SELECT 1 and LISTEN.
func fakePG(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go servePG(conn)
		}
	}()
	return ln.Addr().String()
}

func servePG(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	be := pgproto3.NewBackend(conn, conn)
	if _, err := be.ReceiveStartupMessage(); err != nil {
		return
	}
	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
	be.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	be.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if be.Flush() != nil {
		return
	}
	for {
		msg, err := be.Receive()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			if strings.HasPrefix(strings.ToUpper(m.String), "SELECT") {
				be.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("?column?"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
				be.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			} else {
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("LISTEN")})
			}
			be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if be.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

// unreachableAddr returns a host:port that refuses connections.
func unreachableAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// apiFakes are the HTTP services, recording what they were asked.
type apiFakes struct {
	Anthropic, Gateway, Cloudflare, Clerk, Sandbox, OTLP *httptest.Server

	mu       sync.Mutex
	Direct   []string // models asked directly
	ViaGW    []string // models asked through the gateway
	CFModels []string // models asked on the REST path
	Got      map[string]string
	Status   int // when non-zero, every server answers this
}

func newAPIFakes(t *testing.T, anthropicKey, aigToken string) *apiFakes {
	t.Helper()
	f := &apiFakes{Got: map[string]string{}}
	note := func(k, v string) { f.mu.Lock(); f.Got[k] = v; f.mu.Unlock() }
	status := func(w http.ResponseWriter) bool {
		f.mu.Lock()
		s := f.Status
		f.mu.Unlock()
		if s != 0 {
			// Echo secrets in the body, like a careless upstream, to prove doctor never prints bodies.
			w.WriteHeader(s)
			_, _ = w.Write([]byte(`{"error":"` + "SENTINEL-echo" + `"}`))
			return true
		}
		return false
	}
	count := func(viaGW bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if status(w) {
				return
			}
			if r.URL.Path != "/v1/messages/count_tokens" && r.URL.Path != "/anthropic/v1/messages/count_tokens" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Header.Get("X-Api-Key") != anthropicKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if viaGW && r.Header.Get("cf-aig-authorization") != "Bearer "+aigToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			if viaGW {
				f.ViaGW = append(f.ViaGW, body.Model)
			} else {
				f.Direct = append(f.Direct, body.Model)
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"input_tokens":8}`))
		}
	}
	f.Anthropic = httptest.NewServer(count(false))
	f.Gateway = httptest.NewServer(count(true))

	f.Cloudflare = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status(w) {
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tokens/verify"):
			_, _ = w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
		case strings.HasSuffix(r.URL.Path, "/r2/buckets"):
			note("r2-jurisdiction", r.Header.Get("cf-r2-jurisdiction"))
			_, _ = w.Write([]byte(`{"success":true,"result":{"buckets":[]}}`))
		case strings.HasSuffix(r.URL.Path, "/ai-gateway/gateways"):
			_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
		case strings.HasSuffix(r.URL.Path, "/ai/v1/chat/completions"):
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.CFModels = append(f.CFModels, body.Model)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"choices":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	f.Clerk = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status(w) {
			return
		}
		note("clerk-path", r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	f.Sandbox = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status(w) {
			return
		}
		note("sandbox-path", r.URL.Path)
		note("sandbox-auth", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	f.OTLP = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status(w) {
			return
		}
		data, _ := io.ReadAll(r.Body)
		note("otlp-path", r.URL.Path)
		note("otlp-type", r.Header.Get("Content-Type"))
		note("otlp-auth", r.Header.Get("Authorization"))
		note("otlp-dataset", r.Header.Get("X-Axiom-Dataset"))
		if len(data) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, s := range []*httptest.Server{f.Anthropic, f.Gateway, f.Cloudflare, f.Clerk, f.Sandbox, f.OTLP} {
		t.Cleanup(s.Close)
	}
	return f
}

func (f *apiFakes) setStatus(s int) { f.mu.Lock(); f.Status = s; f.mu.Unlock() }

func (f *apiFakes) endpoints() Endpoints {
	return Endpoints{Anthropic: f.Anthropic.URL, Cloudflare: f.Cloudflare.URL, Clerk: f.Clerk.URL}
}

// newSlowServer never answers within a test's timeout.
func newSlowServer(t *testing.T) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-done }))
	t.Cleanup(func() { close(done); s.Close() })
	return s
}
