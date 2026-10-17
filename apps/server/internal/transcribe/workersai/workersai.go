// Package workersai is the hosted transcription provider (D-27): Cloudflare Workers AI, model @cf/openai/whisper-large-v3-turbo,
// called over its REST API with the environment's account.
//
// It is a stub in the sense that nothing has called Cloudflare yet: there is no account until P0-09. It is complete against the
// documented request and response, unit-tested against a fake server, and switched off unless an account id and an API token are
// configured (the provider then reports transcribe.ErrNotConfigured and the job waits). Two things D-27 wants checked before
// production uses it are not settled here: whether inference stays in the EU (D-15) and the per-minute price, which is why
// CostMicros reports "unpriced" until a price is configured.
//
// Audio longer than ChunkSeconds is sent in consecutive chunks and the times are shifted back; a word that a chunk boundary cuts in
// two is transcribed in both halves. Chunking exists because a request carries the audio base64-encoded in its body.
package workersai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

const (
	// Name is the provider's configuration name.
	Name = "workersai"
	// ModelID is the Workers AI model.
	ModelID = "@cf/openai/whisper-large-v3-turbo"
	// DefaultBaseURL is the Cloudflare API.
	DefaultBaseURL = "https://api.cloudflare.com/client/v4"
	// DefaultChunkSeconds is the longest audio sent in one request: 10 minutes of 16 kHz PCM is 19 MB, 26 MB base64.
	DefaultChunkSeconds = 600

	maxResponseBytes = 32 << 20
)

// Config configures the provider.
type Config struct {
	// AccountID and APIToken are the environment's Cloudflare account and a token with Workers AI read and edit rights
	// (WORKERS_AI_ACCOUNT_ID, WORKERS_AI_API_TOKEN). Either empty switches the provider off.
	AccountID string
	APIToken  string
	// BaseURL replaces DefaultBaseURL (tests).
	BaseURL string
	// Client replaces the HTTP client; nil means one with a five-minute timeout.
	Client *http.Client
	// ChunkSeconds overrides DefaultChunkSeconds.
	ChunkSeconds int
	// PriceMicrosPerMinute is the list price of a minute of audio in millionths of a US dollar. Zero means the price has not been
	// verified: the ledger records the call as unpriced (D-27, "never guess a price").
	PriceMicrosPerMinute int64
	// RequirePrice makes an unset price fatal: the provider reports transcribe.ErrNotConfigured and sends nothing. Staging and
	// production set it (D-27): without a price the ledger would record every call at 0, the daily spend cap could never trip and
	// the cost of a flood of uploads would be invisible until the Cloudflare invoice.
	RequirePrice bool
}

// Provider calls Workers AI.
type Provider struct {
	cfg Config
}

var _ transcribe.Provider = (*Provider)(nil)

// New returns the provider. It never fails and never calls the network: an unconfigured provider says so when it is used.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.ChunkSeconds <= 0 {
		cfg.ChunkSeconds = DefaultChunkSeconds
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Provider{cfg: cfg}
}

// Configured reports whether the provider may be called: an account and a token are set, and, when the price is required, so is
// the price.
func (p *Provider) Configured() bool {
	if p.cfg.RequirePrice && p.cfg.PriceMicrosPerMinute <= 0 {
		return false
	}
	return p.cfg.AccountID != "" && p.cfg.APIToken != ""
}

// Name implements transcribe.Provider.
func (p *Provider) Name() string { return Name }

// Model implements transcribe.Provider.
func (p *Provider) Model() string { return "whisper-large-v3-turbo" }

const (
	priceConfigured = "workers-ai-config"
	priceUnpriced   = "unpriced"
)

// CostMicros implements transcribe.Provider.
func (p *Provider) CostMicros(audioMs int64) (int64, string) {
	if p.cfg.PriceMicrosPerMinute <= 0 {
		return 0, priceUnpriced
	}
	return int64(math.Ceil(float64(audioMs) / 60000 * float64(p.cfg.PriceMicrosPerMinute))), priceConfigured
}

type request struct {
	Audio    string `json:"audio"`
	Task     string `json:"task"`
	Language string `json:"language,omitempty"`
}

type response struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result struct {
		Words []struct {
			Word  string  `json:"word"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"words"`
		Info struct {
			Language string `json:"language"`
		} `json:"transcription_info"`
	} `json:"result"`
}

// Transcribe implements transcribe.Provider.
func (p *Provider) Transcribe(ctx context.Context, audio transcribe.Audio, opts transcribe.Options) (transcribe.Transcript, error) {
	if !p.Configured() {
		return transcribe.Transcript{}, transcribe.ErrNotConfigured
	}
	pcm, err := transcribe.ReadWAV(audio.Path)
	if err != nil {
		return transcribe.Transcript{}, err
	}
	if len(pcm) == 0 {
		return transcribe.Transcript{}, fmt.Errorf("%w: no samples", transcribe.ErrUnusableAudio)
	}
	chunk := p.cfg.ChunkSeconds * transcribe.SampleRate
	var out transcribe.Transcript
	lang := strings.ToLower(strings.TrimSpace(opts.Language))
	for from := 0; from < len(pcm); from += chunk {
		part := pcm[from:min(from+chunk, len(pcm))]
		offset := transcribe.DurationMsOf(from)
		res, err := p.call(ctx, part, lang)
		if err != nil {
			// The chunks before this one were answered, so they were billed: report them with the error.
			return out, err
		}
		out.Calls = append(out.Calls, transcribe.Call{AudioMs: transcribe.DurationMsOf(len(part))})
		if out.Language == "" {
			out.Language = res.Result.Info.Language
			if lang == "" {
				lang = out.Language // the rest of the clip is the same language
			}
		}
		for _, w := range res.Result.Words {
			text := strings.TrimSpace(w.Word)
			if text == "" {
				continue
			}
			s, e := offset+int64(math.Round(w.Start*1000)), offset+int64(math.Round(w.End*1000))
			out.Words = append(out.Words, transcribe.Word{Text: text, StartMs: s, EndMs: max(e, s)})
		}
	}
	if out.Language == "" {
		out.Language = lang
	}
	return out, nil
}

// call sends one chunk.
func (p *Provider) call(ctx context.Context, pcm []int16, lang string) (response, error) {
	body, err := json.Marshal(request{Audio: base64.StdEncoding.EncodeToString(transcribe.EncodeWAV(pcm)), Task: "transcribe", Language: lang})
	if err != nil {
		return response{}, err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/accounts/" + url.PathEscape(p.cfg.AccountID) + "/ai/run/" + ModelID
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("workers ai: %w", scrub(err, p.cfg.APIToken))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return response{}, fmt.Errorf("workers ai: read response: %w", err)
	}
	var out response
	jerr := json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge:
		return response{}, fmt.Errorf("%w: workers ai rejected the audio: %s", transcribe.ErrUnusableAudio, firstError(out, resp.Status))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return response{}, fmt.Errorf("workers ai: the API token was refused (%s)", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return response{}, fmt.Errorf("workers ai: %s", firstError(out, resp.Status)) // 429 and 5xx: the job retries
	case jerr != nil:
		return response{}, fmt.Errorf("workers ai: response: %w", jerr)
	case !out.Success:
		return response{}, fmt.Errorf("workers ai: %s", firstError(out, "the request did not succeed"))
	}
	return out, nil
}

func firstError(r response, fallback string) string {
	if len(r.Errors) > 0 && r.Errors[0].Message != "" {
		return r.Errors[0].Message
	}
	return fallback
}

// scrub keeps the API token out of an error (a transport error can quote the request).
func scrub(err error, token string) error {
	if token == "" {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %s", ue.Op, ue.URL, strings.ReplaceAll(ue.Err.Error(), token, "***"))
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "***"))
}
