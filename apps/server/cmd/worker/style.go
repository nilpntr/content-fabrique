package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/creatorstyle"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// styleLimits bound the sandbox runs of a style reference: a probe, the picture pass and the sound passes over at most 3 minutes of
// video, and the frames of the title read.
func styleLimits() sandbox.Limits {
	return sandbox.Limits{CPU: 2, MemoryMB: 3072, Timeout: 15 * time.Minute, PIDs: 256}
}

// newStyleLearner builds the style learning (P6-05b) for the style jobs: the same sandbox provider, media-tools image and job directory
// root as the ingest, and the model access of the agents for the title read and the summary. It returns nil (the style jobs then wait,
// snoozed) when the runtime is off: a reference measured without its title read would be a worse profile than one that waits.
func newStyleLearner(cfg config.Config, st *store.Store, objects *storage.Client, rt *agentset.Runtime, logger *slog.Logger) (jobs.StyleLearner, error) {
	if rt == nil || objects == nil {
		logger.Warn("style learning is off: it needs object storage and model access; style jobs wait")
		return nil, nil
	}
	if err := os.MkdirAll(cfg.MediaTempDir, 0o750); err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	root, err := filepath.EvalSymlinks(cfg.MediaTempDir)
	if err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	provider, err := newSandboxProvider(cfg, root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "style")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	runner := &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: styleLimits(), Labels: map[string]string{"cf.job": "style_reference"}}
	learner, err := creatorstyle.NewLearner(creatorstyle.LearnerConfig{
		Store: st, Objects: objects, TempDir: dir, Logger: logger, Jobs: jobs.ContextInserter{Logger: logger},
		Analyzer: &styleprofile.Analyzer{Runner: runner, TempDir: dir, Reader: &styleprofile.Reader{Models: rt.Factory, Router: rt.Router, Runner: runner, TempDir: dir}},
		Writer:   &styleprofile.Writer{Models: rt.Factory, Router: rt.Router},
	})
	if err != nil {
		return nil, err
	}
	logger.Info("style learning ready", "profile", rt.Router.DefaultProfile())
	return learner, nil
}
