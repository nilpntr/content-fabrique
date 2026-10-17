package llm

import (
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
)

// Route is where one stage of one profile sends its calls.
type Route struct {
	Profile string
	Stage   string
	Model   string
	// Effort is low, medium, high, xhigh or max; empty leaves the model's default.
	Effort    string
	MaxTokens int
	// Batch says the stage runs through the Batch API where it can.
	Batch bool
}

// Apply sets the model, effort and max_tokens of the request from the route and returns the request.
func (r Route) Apply(req Request) Request {
	req.Model, req.Effort, req.MaxTokens = r.Model, r.Effort, r.MaxTokens
	return req
}

// Router resolves (profile, stage) to a Route from config/routing/*.yaml. Code asks for a stage, never for a model
// ID (architecture 7.5). A run may name its own profile: a test can use v1-start while the environment default stays
// dev-cheap.
type Router struct {
	fsys           fs.FS
	defaultProfile string
	catalog        routing.Catalog

	mu       sync.Mutex
	profiles map[string]routing.Profile
}

// NewRouter loads the model catalogue and checks that the default profile (ROUTING_PROFILE) loads.
func NewRouter(fsys fs.FS, defaultProfile string) (*Router, error) {
	if defaultProfile == "" {
		return nil, errors.New("llm: ROUTING_PROFILE is empty")
	}
	cat, err := routing.LoadCatalog(fsys)
	if err != nil {
		return nil, err
	}
	r := &Router{fsys: fsys, defaultProfile: defaultProfile, catalog: cat, profiles: map[string]routing.Profile{}}
	if _, err := r.profile(defaultProfile); err != nil {
		return nil, err
	}
	return r, nil
}

// DefaultProfile is the environment's profile (ROUTING_PROFILE).
func (r *Router) DefaultProfile() string { return r.defaultProfile }

func (r *Router) profile(name string) (routing.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.profiles[name]; ok {
		return p, nil
	}
	p, err := routing.LoadProfile(r.fsys, name)
	if err != nil {
		return routing.Profile{}, err
	}
	r.profiles[name] = p
	return p, nil
}

// Resolve returns the route of a stage. profile is the run's override; empty uses the default profile. The
// stage is a dotted path such as "editor" or "research.lead". MaxTokens is the profile's value, or
// DefaultMaxTokens when the profile does not set one.
func (r *Router) Resolve(profile, stage string) (Route, error) {
	if profile == "" {
		profile = r.defaultProfile
	}
	p, err := r.profile(profile)
	if err != nil {
		return Route{}, err
	}
	e, err := p.Entry(stage)
	if err != nil {
		return Route{}, err
	}
	if _, ok := r.catalog[e.Model]; !ok {
		return Route{}, fmt.Errorf("llm: profile %q stage %q uses model %q, which config/routing/models.yaml does not list", profile, stage, e.Model)
	}
	route := Route{Profile: profile, Stage: stage, Model: e.Model, Effort: e.Effort, MaxTokens: e.MaxTokens, Batch: e.Batch}
	if route.MaxTokens == 0 {
		route.MaxTokens = DefaultMaxTokens
	}
	return route, nil
}
