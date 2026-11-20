// Command cf-job runs one remote sandbox job inside the media-tools image (docs/design/render-runtime.md, section 3): it
// downloads the job's inputs from presigned URLs to /in, runs each step without a shell under the job's limits with /work as
// its working directory, uploads the outputs to presigned URLs, and reports every step's exit code, stdout, stderr, duration
// and whether it timed out.
//
//	cf-job serve [-addr :8080]            the HTTP contract (PUT, GET, DELETE /jobs/{id}); the token is CF_JOB_TOKEN
//	cf-job run -id ID manifest.json       one job, its status printed as JSON (exit 0 when it succeeded)
//	cf-job health URL                     exit 0 when URL answers 200 (the container health check)
//	cf-job -version
//
// Limits: each step gets RLIMIT_NPROC (Limits.PIDs), RLIMIT_FSIZE (the largest output cap), 1024 open files and no core
// dumps, and is killed with its process group at its timeout. CPU and memory are the container's (compose cpus and
// mem_limit, the Cloudflare instance type): one job runs at a time and a job asking for more than the runner has is refused.
//
// Egress: the runner itself fetches and uploads; CF_JOB_ALLOWED_HOSTS limits that to a list of host:port, and CF_JOB_DIAL
// connects a host:port of the URLs to another address (compose: the worker signs URLs for localhost:9000, which is minio:9000
// from the runner). The tools reach nothing through the runner. It uses the standard library only and is built statically.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const version = "cf-job 1"

func main() {
	if len(os.Args) > 1 && os.Args[1] == execCommand {
		os.Exit(execMain(os.Args[2:]))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cf-job serve | run | -version")
		return 2
	}
	switch args[0] {
	case "-version", "--version", "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		return serveMain(ctx, args[1:], stderr)
	case "run":
		return runMain(ctx, args[1:], stdin, stdout, stderr)
	case "health":
		return healthMain(ctx, args[1:], stderr)
	}
	fmt.Fprintf(stderr, "cf-job: unknown command %q\n", args[0])
	return 2
}

// healthMain is the container's health check (the image has no shell, curl or wget): `cf-job health <url>` exits 0 when the
// URL answers 200 within five seconds.
func healthMain(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: cf-job health http://127.0.0.1:8080/healthz")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args[0], nil) // #nosec G704 -- the health check's own URL, from the container definition
	if err != nil {
		fmt.Fprintf(stderr, "cf-job health: %v\n", err)
		return 2
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- same
	if err != nil {
		fmt.Fprintf(stderr, "cf-job health: %v\n", err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "cf-job health: %s\n", resp.Status)
		return 1
	}
	return 0
}

// runnerFlags are the flags serve and run share.
type runnerFlags struct {
	in, work, tmp string
	cpus          float64
	memoryMB      int
	allowedHosts  string
	dial          string
}

func (f *runnerFlags) register(fs *flag.FlagSet, getenv func(string) string) {
	fs.StringVar(&f.in, "in", InputDir, "where the inputs are downloaded (the image's /in)")
	fs.StringVar(&f.work, "work", WorkDir, "the steps' working directory (the image's /work)")
	fs.StringVar(&f.tmp, "tmp", os.TempDir(), "where each job gets its private HOME and TMPDIR")
	fs.Float64Var(&f.cpus, "cpus", envFloat(getenv("CF_JOB_CPUS")), "the CPUs this runner has (CF_JOB_CPUS); 0 reads the cgroup")
	fs.IntVar(&f.memoryMB, "memory-mb", int(envFloat(getenv("CF_JOB_MEMORY_MB"))), "the memory this runner has in MiB (CF_JOB_MEMORY_MB); 0 reads the cgroup")
	fs.StringVar(&f.allowedHosts, "allowed-hosts", getenv("CF_JOB_ALLOWED_HOSTS"), "comma-separated host:port the inputs and outputs may use (CF_JOB_ALLOWED_HOSTS); empty allows any")
	fs.StringVar(&f.dial, "dial", getenv("CF_JOB_DIAL"), "comma-separated host:port=address: connect there instead (CF_JOB_DIAL)")
}

func (f *runnerFlags) runner() (*Runner, []string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("own binary: %w", err)
	}
	for _, d := range []string{f.in, f.work, f.tmp} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			return nil, nil, fmt.Errorf("%s is not a directory", d)
		}
	}
	hosts := splitList(f.allowedHosts)
	dial := map[string]string{}
	for _, pair := range splitList(f.dial) {
		from, to, ok := strings.Cut(pair, "=")
		if !ok || from == "" || to == "" {
			return nil, nil, fmt.Errorf("-dial: %q is not host:port=address", pair)
		}
		dial[from] = to
	}
	cpus, mem := f.cpus, f.memoryMB
	if cpus == 0 {
		cpus = cgroupCPUs()
	}
	if mem == 0 {
		mem = cgroupMemoryMB()
	}
	return &Runner{InDir: f.in, WorkDir: f.work, TmpDir: f.tmp, CPUs: cpus, MemoryMB: mem, Client: newClient(hosts, dial), Self: self}, hosts, nil
}

func serveMain(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("cf-job serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var rf runnerFlags
	rf.register(fs, os.Getenv)
	addr := fs.String("addr", ":8080", "the listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	token := os.Getenv("CF_JOB_TOKEN")
	if len(token) < 16 {
		fmt.Fprintln(stderr, "cf-job serve: CF_JOB_TOKEN must hold the bearer token (16 characters or more)")
		return 2
	}
	r, hosts, err := rf.runner()
	if err != nil {
		fmt.Fprintf(stderr, "cf-job serve: %v\n", err)
		return 2
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "cf-job serve: %v\n", err)
		return 1
	}
	jobs, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	s := newServer(jobs, r, token, hosts)
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute}
	fmt.Fprintf(stderr, "cf-job serve: listening on %s (cpus %v, memory %d MiB)\n", ln.Addr(), r.CPUs, r.MemoryMB)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		fmt.Fprintf(stderr, "cf-job serve: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	// SIGTERM: stop taking requests, stop the running job (its processes and files go with it).
	shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	cancelJobs()
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur != nil {
		select {
		case <-cur.done:
		case <-shut.Done():
		}
	}
	return 0
}

func runMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cf-job run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var rf runnerFlags
	rf.register(fs, os.Getenv)
	id := fs.String("id", "run", "the job id")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || !validID(*id) {
		fmt.Fprintln(stderr, "usage: cf-job run [-id ID] manifest.json (or - for stdin)")
		return 2
	}
	var data []byte
	var err error
	if fs.Arg(0) == "-" {
		data, err = io.ReadAll(io.LimitReader(stdin, maxManifestBytes+1))
	} else {
		data, err = os.ReadFile(fs.Arg(0))
	}
	if err == nil && len(data) > maxManifestBytes {
		err = errors.New("the manifest is too large")
	}
	var m Manifest
	if err == nil {
		m, err = parseManifest(data)
	}
	r, hosts, rerr := rf.runner()
	if err == nil {
		err = rerr
	}
	if err == nil {
		err = checkHosts(hosts, m)
	}
	if err == nil {
		err = r.checkCapacity(m)
	}
	if err != nil {
		fmt.Fprintf(stderr, "cf-job run: %v\n", err)
		return 2
	}
	j := newJob(ctx, *id, m)
	r.execute(j)
	st := j.status()
	writeStatus(stdout, st)
	if st.State != StateSucceeded {
		return 1
	}
	return 0
}

func writeStatus(w io.Writer, st Status) { _ = json.NewEncoder(w).Encode(st) }

// newClient is the runner's HTTP client: it connects only to the allowed hosts (when there is a list), through the dial
// overrides, never through a proxy, and follows no redirects (a presigned URL never redirects).
func newClient(allowed []string, dial map[string]string) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	allowedSet := map[string]bool{}
	for _, h := range allowed {
		allowedSet[h] = true
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if len(allowedSet) > 0 && !allowedSet[addr] {
				return nil, fmt.Errorf("egress to %s is not allowed", addr)
			}
			if to, ok := dial[addr]; ok {
				addr = to
			}
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          4,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// cgroupCPUs is the container's CPU limit from cgroup v2 cpu.max ("max 100000" or "400000 100000"); 0 when unknown.
func cgroupCPUs() float64 {
	b, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] == "max" {
		return 0
	}
	quota, err1 := strconv.ParseFloat(f[0], 64)
	period, err2 := strconv.ParseFloat(f[1], 64)
	if err1 != nil || err2 != nil || period <= 0 {
		return 0
	}
	return quota / period
}

// cgroupMemoryMB is the container's memory limit from cgroup v2 memory.max, in MiB; 0 when unknown or unlimited.
func cgroupMemoryMB() int {
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return int(v >> 20)
}
