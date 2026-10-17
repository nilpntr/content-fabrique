// Package transcribe turns the speech of an asset into a word-level transcript (P2-05, decision D-27, D-28).
//
// One Provider interface hides where the model runs: whisper.cpp in the media-tools sandbox (package whispercpp, local, ci and
// bench) or Workers AI (package workersai, staging and production, behind configuration). Captions and the editor never see which
// one was used. Around the provider this package holds what does not depend on it:
//
//   - a speech check first (Detector): transcribing a clip without speech costs money and invents words, so the audio is checked
//     with a voice-activity model before any provider is called;
//   - Refine, which moves the word boundaries a provider estimated onto the audio itself (the best word timings of an attention
//     based model are good for roughly a word, not for a jump cut);
//   - Structure: sentence boundaries, filler words and pauses, the speech structure the editor cuts on (D-28);
//   - the model cache (models.go): model files are not in the image, they are downloaded once and checked against a pinned SHA-256;
//   - the job's pipeline (pipeline.go).
package transcribe

import (
	"context"
	"errors"
)

// Word is one spoken word with its place in the audio.
type Word struct {
	// Text is the word as the model wrote it, punctuation included ("Hello,").
	Text string `json:"text"`
	// StartMs and EndMs are milliseconds from the start of the audio. EndMs >= StartMs, and words do not overlap.
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
	// Confidence is the model's probability for the word, 0 to 1; 0 when the provider does not report one.
	Confidence float64 `json:"confidence"`
	// AnchorMs is set by a provider whose StartMs and EndMs are only rough: a time it is confident lies inside the word. Refine
	// uses it to place the real boundaries and clears it. It is never stored.
	AnchorMs int64 `json:"-"`
}

// Transcript is what a provider returns.
type Transcript struct {
	// Language is the ISO 639-1 code of the spoken language ("en"), as detected or as asked for.
	Language string
	Words    []Word
	// Calls are the requests the provider made that were, or may have been, billed: one for a local run, one per chunk for a hosted
	// one. A provider that fails part way returns the calls it had already made together with the error, so paid work is always
	// metered (the pipeline writes one ledger row per call). A call that failed before the provider answered is not listed.
	Calls []Call
}

// Call is one billed request to a provider.
type Call struct {
	// AudioMs is the length of the audio the request carried.
	AudioMs int64
	// Estimated says the request was sent and its outcome is unknown (a timeout, a body that could not be read, a 200 that could not be
	// parsed): it may have been billed, so it is recorded at the price reserved for its audio and marked estimated in the ledger, the
	// way the model client records a lost call.
	Estimated bool
}

// Audio is the input of a provider: a 16 kHz mono signed 16-bit PCM WAV file on the host (the audio_16k.wav derivative).
type Audio struct {
	// Path is the file. A sandboxed provider mounts it read-only; a hosted one reads it.
	Path string
	// DurationMs is its length.
	DurationMs int64
}

// Options are the per-call settings.
type Options struct {
	// Language is an ISO 639-1 code, or empty to detect it.
	Language string
}

// Provider transcribes audio into words with timings.
type Provider interface {
	// Name is the provider's name as configured and as the ledger records it ("whispercpp", "workersai").
	Name() string
	// Model names the model that does the work ("ggml-large-v3-turbo-q5_0"), for the transcript and the ledger.
	Model() string
	// Transcribe returns the words of the audio, in order. A clip with no speech may come back with no words (a hallucination
	// filter is the caller's business: the pipeline only calls it after a speech check). On an error it still returns the
	// Transcript's Calls, for the billed work that was done before the failure.
	Transcribe(ctx context.Context, audio Audio, opts Options) (Transcript, error)
	// CostMicros is what transcribing audioMs of audio costs at list price, in millionths of a US dollar, and the version of the
	// price table that says so. Local providers cost 0 ("local"); a hosted provider without a verified price reports
	// "unpriced" and 0 (D-27: never guess a price).
	CostMicros(audioMs int64) (micros int64, priceVersion string)
}

// Segment is a stretch of audio with speech in it.
type Segment struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

// Detector says where the speech is. It is cheap: a small voice-activity model, not a transcriber.
type Detector interface {
	DetectSpeech(ctx context.Context, audio Audio) ([]Segment, error)
}

// Sentinel errors providers return, so the pipeline can tell a bad input from a broken provider.
var (
	// ErrUnusableAudio is audio the provider cannot work with and never will (not 16 kHz PCM, empty, too large). The job gives up
	// instead of retrying.
	ErrUnusableAudio = errors.New("transcribe: the audio cannot be transcribed")
	// ErrNotConfigured is a provider that is switched off: no account, no token, or (hosted, staging and production) no verified
	// price, so its spend could not be capped. The job waits (snoozes) instead of failing, and nothing is sent.
	ErrNotConfigured = errors.New("transcribe: the provider is not configured")
)

// MinSpeechMs is how much speech the voice-activity check must find before a clip counts as having any: shorter than this is a
// cough, a click or a door.
const MinSpeechMs = 500

// SpeechMs adds up the speech segments.
func SpeechMs(segs []Segment) int64 {
	var n int64
	for _, s := range segs {
		n += max(s.EndMs-s.StartMs, 0)
	}
	return n
}
