package render

import "github.com/nilpntr/content-fabrique/internal/subject"

// Word is one spoken word of an asset's transcript: its text and where it is in the asset, in milliseconds. Transcripts come
// from the ingest pipeline (P2-05); the renderer only takes them through the resolver, so any source of words with times
// (a fixture in a test, a stored transcript later) can drive the captions.
type Word struct {
	Text    string `json:"text"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// ResolvedAsset is where ffmpeg reads an asset from.
type ResolvedAsset struct {
	// Location is the absolute path of a local file: the sandbox has no network, so the worker stages an asset (the proxy
	// or the original) before the render and the resolver returns where it put it.
	Location string
	// HasAudio says whether the file has an audio stream. A clip that asks for its source audio but has none plays
	// silence instead of failing the render.
	HasAudio bool
	// Transcript is the asset's words with their times in the asset, in order. The captions track draws the words of the
	// part of the asset each clip plays; an asset without a transcript has no captions.
	Transcript []Word
	// Subject is where the person is in the asset's sampled frames (package subject), when the quick look read it: the titles that would
	// cover the head are moved off it. Nil for an asset without one.
	Subject *subject.Track
}

// AssetResolver maps an asset id to the file a profile reads: the proxy for the preview, the original for the final
// render. The compiler calls it once per distinct asset, in the order the assets first appear in the spec.
type AssetResolver interface {
	Resolve(assetID string, variant Variant) (ResolvedAsset, error)
}

// ResolverFunc adapts a function to AssetResolver.
type ResolverFunc func(assetID string, variant Variant) (ResolvedAsset, error)

// Resolve implements AssetResolver.
func (f ResolverFunc) Resolve(assetID string, variant Variant) (ResolvedAsset, error) {
	return f(assetID, variant)
}
