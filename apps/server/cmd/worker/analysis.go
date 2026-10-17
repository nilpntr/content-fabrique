package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// newAnalysis builds the clip analysis for the analyze job: the same sandbox provider, media-tools image and job directory root
// as the ingest, with limits sized for one decode pass over a 720 px proxy. The pass is bounded in time by the service, which
// scales it with the clip's length.
func newAnalysis(cfg config.Config, st *store.Store, objects *storage.Client, logger *slog.Logger) (jobs.Analyzer, error) {
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
	dir := filepath.Join(root, "analysis")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	limits := analysisLimits()
	svc, err := analysis.New(analysis.Config{
		Store: st, Objects: objects, TempDir: dir, Logger: logger,
		Runner: &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: limits, Labels: map[string]string{"cf.job": "analyze"}},
	})
	if err != nil {
		return nil, err
	}
	logger.Info("clip analysis ready", "version", analysis.Version, "concurrency", jobs.DefaultAnalysisConcurrency)
	return svc, nil
}
