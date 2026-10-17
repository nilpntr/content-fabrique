// Package agentset builds what a process needs to start and run agents: the model-access layer (settings, price table, router,
// client factory), the agent definitions with their services, and the runtime Service and Runner over them. The api, the worker
// and cfctl all get their agents from here, so they cannot disagree about what an agent name means (the echo agent and the
// editor are built here because the editor's package imports the runtime, so runtime.DefaultAgents cannot name it).
package agentset

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/annotations"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ErrNoStorage is returned by Open for a process that is to run agents but has no object storage: the editor cannot look at
// footage without it.
var ErrNoStorage = errors.New("agentset: object storage is not configured, so the editor agent cannot run")

// Options say what to build.
type Options struct {
	Store  *store.Store
	Logger *slog.Logger
	// Getenv reads the settings (LLM_MODE, ROUTING_PROFILE, ANTHROPIC_API_KEY, ...): os.Getenv, or cfctl's merged view.
	Getenv func(string) string
	// DefaultProfile is the routing profile when ROUTING_PROFILE is empty; empty means dev-cheap.
	DefaultProfile string
	// Enqueuer enqueues the agent_run job when a run starts. Nil with Inline false means Start fails; cfctl runs the loop itself
	// (Inline) and needs none.
	Enqueuer *jobs.Enqueuer
	Inline   bool
	// DefinitionOnly builds the agents so that runs can be created and checked, but never executed: the api. Objects, Frames and
	// TempDir are then not needed, and no Runner is built.
	DefinitionOnly bool
	// RequireStorage makes Open fail with ErrNoStorage when the editor would have no object storage and sandbox (the worker,
	// whose jobs then wait for a worker that has them). Without it, the editor is defined with tools that cannot look at footage,
	// which is enough for the echo agent in tests and for the dev CLI.
	RequireStorage bool
	// Objects is object storage: the editor reads proxies from it and keeps its frames in it.
	Objects *storage.Client
	// Frames runs ffmpeg for the editor's frames (see NewFrameRunner) in TempDir.
	Frames  media.Runner
	TempDir string
	// Preview renders the editor's previews; nil means they are not available yet.
	Preview editor.PreviewRenderer
	// HTTPClient replaces the model API's transport (tests).
	HTTPClient *http.Client
}

// Runtime is the result of Open.
type Runtime struct {
	Settings llm.Settings
	Router   *llm.Router
	Factory  *llm.Factory
	Agents   *runtime.Agents
	// Frames is the frame service the editor looks at footage with (and the quick look cuts its candidates with): the same
	// cache of pictures in object storage. Nil when the process has no object storage and sandbox for it.
	Frames *editor.FrameService
	// Service starts, answers and cancels runs.
	Service *runtime.Service
	// Runner executes runs; nil for a DefinitionOnly runtime.
	Runner *runtime.Runner
}

// Open builds the runtime. Every failure says which setting is wrong.
func Open(o Options) (*Runtime, error) {
	if o.Store == nil || o.Getenv == nil {
		return nil, errors.New("agentset: a store and Getenv are required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	settings, err := llm.LoadSettings(o.Getenv)
	if err != nil {
		return nil, fmt.Errorf("model-access settings: %w", err)
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		return nil, fmt.Errorf("price table: %w", err)
	}
	router, err := llm.NewRouter(configfiles.Routing(), cmp.Or(settings.RoutingProfile, o.DefaultProfile, "dev-cheap"))
	if err != nil {
		return nil, fmt.Errorf("routing: %w", err)
	}

	projectsSvc, err := projects.NewService(o.Store, o.Logger)
	if err != nil {
		return nil, err
	}
	deps := editor.Deps{Projects: projectsSvc, Store: o.Store, Preview: o.Preview}
	// The creator's comments reach the editor through the time maps of the renders they were made on, read from object storage; a
	// process without storage still lists them, unmapped.
	comments := &annotations.Source{Store: o.Store, Logger: o.Logger}
	if o.Objects != nil {
		comments.TimeMaps = annotations.ObjectTimeMaps{Objects: o.Objects}
	}
	deps.Comments = comments
	var (
		images llm.ImageResolver
		frames *editor.FrameService
	)
	switch {
	case o.Objects != nil && o.Frames != nil && o.TempDir != "":
		frames = &editor.FrameService{Store: o.Store, Objects: o.Objects, Runner: o.Frames, TempDir: o.TempDir, Logger: o.Logger}
		deps.Frames = frames
		images = editor.ImageResolver{Objects: o.Objects}
	case o.RequireStorage && !o.DefinitionOnly:
		return nil, ErrNoStorage
	default:
		deps.Frames = noFrames{}
	}
	editorAgent, err := editor.NewAgent(deps)
	if err != nil {
		return nil, err
	}
	agents, err := runtime.NewAgents(runtime.DefaultAgents(editorAgent)...)
	if err != nil {
		return nil, fmt.Errorf("agent definitions: %w", err)
	}

	factory, err := llm.NewFactory(llm.FactoryOptions{
		Settings: settings, Getenv: o.Getenv, Prices: prices, Ledger: o.Store, Images: images, HTTPClient: o.HTTPClient, Logger: o.Logger,
	})
	if err != nil {
		return nil, err
	}
	rt := &Runtime{
		Settings: settings, Router: router, Factory: factory, Agents: agents, Frames: frames,
		Service: &runtime.Service{Store: o.Store, Enqueuer: o.Enqueuer, Router: router, Factory: factory, Agents: agents, Inline: o.Inline},
	}
	if !o.DefinitionOnly {
		if rt.Runner, err = runtime.NewRunner(runtime.Deps{Store: o.Store, Factory: factory, Agents: agents, Logger: o.Logger}); err != nil {
			return nil, err
		}
	}
	return rt, nil
}

// noFrames is the frame source of a process that only defines the editor (the api) or has no object storage (the dev CLI).
type noFrames struct{}

func (noFrames) Frames(context.Context, ids.WorkspaceID, ids.AssetID, []int64) ([]editor.Frame, error) {
	return nil, errors.New("agentset: this process has no object storage and sandbox to take frames with")
}

// NewFrameRunner builds the sandbox runner the editor takes its frames with, and the directory it works in. provider is
// "docker", "hardened" or "local" (MEDIA_SANDBOX_PROVIDER), image the media-tools image, tempRoot MEDIA_TEMP_DIR and cfEnv the
// environment (the local provider isolates nothing and is refused in staging and production). The directory is created and
// resolved so that the sandbox's allowed root has no symlinks in it.
func NewFrameRunner(provider, image, tempRoot, cfEnv string) (media.Runner, string, error) {
	if err := os.MkdirAll(tempRoot, 0o750); err != nil {
		return nil, "", fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	root, err := filepath.EvalSymlinks(tempRoot)
	if err != nil {
		return nil, "", fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	pol := sandbox.Policy{AllowedRoots: []string{root}}
	var p sandbox.Provider
	switch provider {
	case config.SandboxDocker:
		p = sandbox.NewDocker(pol)
	case config.SandboxHardened:
		p = sandbox.NewHardened(pol)
	case config.SandboxLocal:
		if err := sandbox.CheckLocalAllowed(cfEnv); err != nil {
			return nil, "", err
		}
		p = sandbox.NewLocal(pol)
	default:
		return nil, "", fmt.Errorf("MEDIA_SANDBOX_PROVIDER: unknown provider %q", provider)
	}
	dir := filepath.Join(root, "frames")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	limits := sandbox.Limits{CPU: 2, MemoryMB: 2048, Timeout: 2 * time.Minute, PIDs: 256}
	runner := &media.SandboxRunner{Provider: p, Image: image, Limits: limits, Labels: map[string]string{"cf.job": "editor_frames"}}
	return runner, dir, nil
}
