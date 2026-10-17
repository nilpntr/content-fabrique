package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	configfiles "github.com/nilpntr/content-fabrique/config"
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
	if ji, ok := jobs.JobInfoFrom(ctx); ok {
		ctx = transcribe.WithJob(ctx, transcribe.JobRef{ID: ji.ID, Attempt: ji.Attempt})
	}
	res, err := t.p.Run(ctx, ws, id, generation)
	switch {
	case errors.Is(err, transcribe.ErrPlatformPaused):
		return nil, errors.Join(jobs.ErrTranscribePaused, err)
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
	// The caps are the model client's (DAILY_SPEND_CAP_MICROS, PLATFORM_DAILY_SPEND_CAP_MICROS): one budget for editing and
	// transcription, enforced by the same holds and counters in the database. Settings or prices that cannot be read leave
	// transcription off (the jobs wait) rather than running on a cap or a price nobody chose.
	settings, err := llm.LoadSettings(os.Getenv)
	if err != nil {
		logger.Error("transcription is off: the model settings (DAILY_SPEND_CAP_MICROS and the rest) cannot be read, so its spend cap is unknown", "error", err)
		return nil, nil
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		logger.Error("transcription is off: the price table cannot be read, so its spend cannot be metered", "error", err)
		return nil, nil
	}
	parts, err := setup.Build(cfg, prices)
	if err != nil {
		return nil, err
	}
	p, err := setup.Pipeline(cfg, parts, st, objects, setup.Gate(cfg, settings, st, logger), logger)
	if err != nil {
		return nil, err
	}
	return transcriber{p: p}, nil
}
