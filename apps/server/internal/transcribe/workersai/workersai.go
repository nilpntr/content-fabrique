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
	"net"
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
	// PriceMicrosPerMinute is the list price of a minute of audio in millionths of a US dollar: transcription.workers_ai.per_minute of
	// the price table, or the operator's WORKERS_AI_PRICE_MICROS_PER_MINUTE override. Zero means no verified price: the ledger would
	// record the call as unpriced (D-27, "never guess a price"), and with RequirePrice the provider does not run.
	PriceMicrosPerMinute int64
	// PriceVersion names where the price came from (the price table's version, or that and "+env" for the override); it is the ledger's
	// price_table_version. Empty means "workers-ai-config".
	PriceVersion string
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

// MaxDuration is the longest transcribing audioMs of audio can take: one request per chunk, each up to the client's timeout. The
// reservation in the database lives that long (plus a margin) so a crashed worker's hold stops blocking the caps afterwards.
func (p *Provider) MaxDuration(audioMs int64) time.Duration {
	chunkMs := int64(p.cfg.ChunkSeconds) * 1000
	chunks := max((audioMs+chunkMs-1)/chunkMs, 1)
	return time.Duration(chunks) * max(p.cfg.Client.Timeout, time.Minute)
}

// CostMicros implements transcribe.Provider.
func (p *Provider) CostMicros(audioMs int64) (int64, string) {
	if p.cfg.PriceMicrosPerMinute <= 0 {
		return 0, priceUnpriced
	}
	version := p.cfg.PriceVersion
	if version == "" {
		version = priceConfigured
	}
	return (audioMs*p.cfg.PriceMicrosPerMinute + 59_999) / 60_000, version
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
		res, unknown, err := p.call(ctx, part, lang)
		if err != nil {
			// The chunks before this one were answered, so they were billed: report them with the error. This one may have been billed
			// too when its request went out and no answer came back (a timeout, a body that could not be read, a 200 that could not be
			// parsed): it is reported as an estimated call, recorded at its reserved price.
			if unknown {
				out.Calls = append(out.Calls, transcribe.Call{AudioMs: transcribe.DurationMsOf(len(part)), Estimated: true})
			}
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

// call sends one chunk. unknown says the request went out (or may have) and no usable answer came back, so the chunk may have been
// billed. A definite refusal (an error status, a dial failure) is not.
func (p *Provider) call(ctx context.Context, pcm []int16, lang string) (out response, unknown bool, err error) {
	body, err := json.Marshal(request{Audio: base64.StdEncoding.EncodeToString(transcribe.EncodeWAV(pcm)), Task: "transcribe", Language: lang})
	if err != nil {
		return response{}, false, err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/accounts/" + url.PathEscape(p.cfg.AccountID) + "/ai/run/" + ModelID
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return response{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return response{}, !neverSent(err), fmt.Errorf("workers ai: %w", scrub(err, p.cfg.APIToken))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return response{}, true, fmt.Errorf("workers ai: read response: %w", err)
	}
	jerr := json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge:
		return response{}, false, fmt.Errorf("%w: workers ai rejected the audio: %s", transcribe.ErrUnusableAudio, firstError(out, resp.Status))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return response{}, false, fmt.Errorf("workers ai: the API token was refused (%s)", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return response{}, false, fmt.Errorf("workers ai: %s", firstError(out, resp.Status)) // 429 and 5xx: the job retries
	case jerr != nil:
		// A 200 whose body is not the answer: the work may have been done and billed.
		return response{}, true, fmt.Errorf("workers ai: response: %w", jerr)
	case !out.Success:
		return response{}, false, fmt.Errorf("workers ai: %s", firstError(out, "the request did not succeed"))
	}
	return out, false, nil
}

// neverSent reports a transport failure that happened before the request could have reached the server: the connection was never
// made (a dial or DNS failure). Anything else (a reset, a timeout, a cancelled context, a TLS failure half way) may have sent it.
func neverSent(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	return errors.As(err, &dns)
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
