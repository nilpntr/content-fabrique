// Package doctor implements `cfctl doctor`: it checks that the environment's variables are present and well
// formed, then makes one cheap authenticated call per service (docs/06-setup-and-credentials.md §6).
//
// Two rules shape the code:
//
//   - Which variables are required comes from the tags in .env.example, parsed at run time. Nothing here keeps its
//     own list of variable names to require, only per-name format checks and a few service wiring details.
//   - A value is never printed. Reasons are built from fixed categories (unreachable, timeout, auth rejected,
//     HTTP <code>, bad format), never from error strings, which can embed DSNs and keys. As a second guard the whole
//     output goes through a Redactor that blanks every loaded value of six or more characters.
package doctor

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
	"github.com/nilpntr/content-fabrique/internal/envfile"
)

// Status is the outcome for one variable or service.
type Status string

// The four statuses. Missing and invalid make the run fail.
const (
	OK      Status = "ok"
	Missing Status = "missing"
	Invalid Status = "invalid"
	Skipped Status = "skipped"
)

// Result is one line of the report.
type Result struct {
	// Kind is "variable" or "service".
	Kind string `json:"kind"`
	// Name is the variable name, or the service check ("postgres DATABASE_URL", "anthropic claude-haiku-5-5").
	Name   string `json:"name"`
	Status Status `json:"status"`
	// Reason is a short category, never a value.
	Reason string `json:"reason,omitempty"`
}

// Report is the outcome of a run.
type Report struct {
	Env     string   `json:"env"`
	Phase   string   `json:"phase"`
	Results []Result `json:"results"`
}

// Failed reports whether any result is missing or invalid.
func (r Report) Failed() bool {
	for _, x := range r.Results {
		if x.Status == Missing || x.Status == Invalid {
			return true
		}
	}
	return false
}

// Count returns how many results have the status.
func (r Report) Count(s Status) int {
	n := 0
	for _, x := range r.Results {
		if x.Status == s {
			n++
		}
	}
	return n
}

// Environments doctor accepts for --env.
var Environments = []string{"local", "ci", "bench", "staging", "production"}

// Endpoints are the third-party base URLs. Empty fields take the real defaults; tests point them at httptest servers.
type Endpoints struct {
	// Anthropic is the direct API. The direct check never uses ANTHROPIC_BASE_URL.
	Anthropic string
	// Cloudflare is the Cloudflare API, including /client/v4.
	Cloudflare string
	// Clerk is the Clerk Backend API origin.
	Clerk string
}

const (
	defaultAnthropic  = "https://api.anthropic.com"
	defaultCloudflare = "https://api.cloudflare.com/client/v4"
	defaultClerk      = "https://api.clerk.com"
)

// Options configures Run.
type Options struct {
	// Env is one of Environments.
	Env string
	// Phase is the number in P0..P7: later-phase variables become required, and the sandbox gateway is checked from 4.
	Phase int
	// Example is the parsed .env.example.
	Example []envfile.Var
	// Vars is the merged environment (see MergeEnv). Names not in Example are ignored.
	Vars map[string]string
	// RoutingFS is the apps/server/config/routing directory.
	RoutingFS fs.FS
	// HTTPClient is used for every HTTP call. Nil means a client with Timeout and without redirects.
	HTTPClient *http.Client
	Endpoints  Endpoints
	// Timeout bounds each check. Zero means 15 seconds.
	Timeout time.Duration
}

// Run performs every check and returns the report. It does not print.
func Run(ctx context.Context, opts Options) Report {
	r := newRun(opts)
	rep := Report{Env: opts.Env, Phase: fmt.Sprintf("P%d", opts.Phase)}
	rep.Results = append(rep.Results, r.checkVariables()...)

	jobs := r.serviceJobs()
	out := make([][]Result, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jctx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()
			out[i] = job(jctx)
		}()
	}
	wg.Wait()
	for _, rs := range out {
		rep.Results = append(rep.Results, rs...)
	}
	rep.Results = append(rep.Results, r.groupResults()...)
	return rep
}

// run holds the state of one doctor run. It is read-only once the service checks start.
type run struct {
	opts      Options
	vars      map[string]string
	byName    map[string]envfile.Var
	client    *http.Client
	timeout   time.Duration
	ep        Endpoints
	varStatus map[string]Status

	// The routing profile and model catalogue, each read once per run.
	routingOnce, catalogOnce sync.Once
	models                   []string
	modelsErr                error
	catalog                  routing.Catalog
	catalogErr               error
}

func newRun(opts Options) *run {
	r := &run{
		opts:      opts,
		vars:      map[string]string{},
		byName:    map[string]envfile.Var{},
		timeout:   opts.Timeout,
		ep:        opts.Endpoints,
		varStatus: map[string]Status{},
	}
	if r.timeout == 0 {
		r.timeout = 15 * time.Second
	}
	if r.ep.Anthropic == "" {
		r.ep.Anthropic = defaultAnthropic
	}
	if r.ep.Cloudflare == "" {
		r.ep.Cloudflare = defaultCloudflare
	}
	if r.ep.Clerk == "" {
		r.ep.Clerk = defaultClerk
	}
	for _, v := range opts.Example {
		r.byName[v.Name] = v
		r.vars[v.Name] = strings.TrimSpace(opts.Vars[v.Name])
	}
	r.client = opts.HTTPClient
	if r.client == nil {
		r.client = &http.Client{Timeout: r.timeout}
	}
	// Never follow a redirect: a credential must only go where the user configured it.
	c := *r.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r.client = &c
	return r
}

// getenv returns the value of a variable from the merged environment. internal/envlint sees the literal names
// passed to it, so every name used here is checked against .env.example in CI.
func (r *run) getenv(name string) string { return r.vars[name] }

// MergeEnv builds the environment doctor checks. A non-empty value in the env file is authoritative; the process
// environment fills every name the file lacks or leaves empty (an empty line in .env.example's copy is a blank to
// fill, not a choice). Only names listed in .env.example are taken from the process.
func MergeEnv(example []envfile.Var, file map[string]string, process func(string) (string, bool)) map[string]string {
	out := map[string]string{}
	for _, v := range example {
		if val := file[v.Name]; val != "" {
			out[v.Name] = val
			continue
		}
		if process != nil {
			if val, ok := process(v.Name); ok && val != "" {
				out[v.Name] = val
				continue
			}
		}
		if val, ok := file[v.Name]; ok {
			out[v.Name] = val
		}
	}
	return out
}

// ParsePhase turns "P4" (or "p4") into 4.
func ParsePhase(s string) (int, error) {
	if len(s) == 2 && (s[0] == 'P' || s[0] == 'p') && s[1] >= '0' && s[1] <= '7' {
		return int(s[1] - '0'), nil
	}
	return 0, fmt.Errorf("phase must be one of P0 to P7")
}
