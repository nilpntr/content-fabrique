package render

// ResolvedAsset is where ffmpeg reads an asset from.
type ResolvedAsset struct {
	// Location is the absolute path of a local file: the sandbox has no network, so the worker stages an asset (the proxy
	// or the original) before the render and the resolver returns where it put it.
	Location string
	// HasAudio says whether the file has an audio stream. A clip that asks for its source audio but has none plays
	// silence instead of failing the render.
	HasAudio bool
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
