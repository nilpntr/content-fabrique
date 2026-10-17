package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
	"github.com/nilpntr/content-fabrique/internal/transcribe/setup"
)

// transcriber adapts the pipeline of package transcribe to jobs.Transcriber: it maps the pipeline's errors to the ones the job acts
// on.
type transcriber struct{ p *transcribe.Pipeline }

var _ jobs.Transcriber = transcriber{}

func (t transcriber) Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) (any, error) {
	res, err := t.p.Run(ctx, ws, id, generation)
	switch {
	case errors.Is(err, transcribe.ErrAssetGone):
		return nil, jobs.ErrTranscribeGone
	case errors.Is(err, transcribe.ErrNotConfigured):
		return nil, errors.Join(jobs.ErrTranscribeNotConfigured, err)
	}
	return res, err
}

func (t transcriber) Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) error {
	return t.p.Fail(ctx, ws, id, generation)
}

// newTranscriber builds the transcription pipeline for the transcribe job: the sandbox provider with the model cache next to the job
// directory, the provider TRANSCRIBE_PROVIDER names and whisper.cpp's voice-activity check. It downloads nothing here: models arrive
// on first use.
func newTranscriber(cfg config.Config, st *store.Store, objects *storage.Client, logger *slog.Logger) (jobs.Transcriber, error) {
	parts, err := setup.Build(cfg)
	if err != nil {
		return nil, err
	}
	// The daily spend cap is the model client's (DAILY_SPEND_CAP_MICROS): one budget for editing and transcription, read the same way.
	// Settings that cannot be read leave transcription off (the jobs wait) rather than running on a cap nobody chose.
	settings, err := llm.LoadSettings(os.Getenv)
	if err != nil {
		logger.Error("transcription is off: the model settings (DAILY_SPEND_CAP_MICROS and the rest) cannot be read, so its spend cap is unknown", "error", err)
		return nil, nil
	}
	p, err := setup.Pipeline(cfg, parts, st, objects, settings.DailySpendCapMicros, logger)
	if err != nil {
		return nil, err
	}
	return transcriber{p: p}, nil
}
