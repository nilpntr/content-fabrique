package timeline

import (
	"encoding/json"
	"fmt"
	"os"
)

// AssetKind is what an asset is.
type AssetKind string

// The asset kinds the catalog reports.
const (
	AssetVideo AssetKind = "video"
	AssetAudio AssetKind = "audio"
)

// Rights is the rights state of an asset.
type Rights string

// The rights states: a licence record exists, the creator attested to owning it, or neither.
const (
	RightsLicensed Rights = "licensed"
	RightsAttested Rights = "attested"
	RightsNone     Rights = "none"
)

// AssetCatalog is what Validate needs to know about the platform's assets. The server implements it over the assets
// table; tests use MemoryCatalog.
type AssetCatalog interface {
	// Duration returns the asset's duration in milliseconds, and false if the asset is not known.
	Duration(assetID string) (ms int, ok bool)
	// Rights returns the asset's kind and rights, and false if the asset is not known.
	Rights(assetID string) (kind AssetKind, rights Rights, ok bool)
}

// AssetInfo is one asset of a MemoryCatalog. The JSON tags match fixtures/assets.json.
type AssetInfo struct {
	DurationMs int       `json:"duration_ms"`
	Kind       AssetKind `json:"kind"`
	Rights     Rights    `json:"rights"`
}

// MemoryCatalog is an in-memory AssetCatalog keyed by asset id.
type MemoryCatalog map[string]AssetInfo

// Duration implements AssetCatalog.
func (c MemoryCatalog) Duration(assetID string) (int, bool) {
	a, ok := c[assetID]
	return a.DurationMs, ok
}

// Rights implements AssetCatalog.
func (c MemoryCatalog) Rights(assetID string) (AssetKind, Rights, bool) {
	a, ok := c[assetID]
	return a.Kind, a.Rights, ok
}

// ParseMemoryCatalog reads the format of fixtures/assets.json: {"assets": {"<asset id>": {"duration_ms", "kind",
// "rights"}}}. Other top-level keys (the file's description) are ignored.
func ParseMemoryCatalog(data []byte) (MemoryCatalog, error) {
	var f struct {
		Assets map[string]AssetInfo `json:"assets"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("asset catalog: %w", err)
	}
	return MemoryCatalog(f.Assets), nil
}

// LoadMemoryCatalog reads a catalog file such as fixtures/assets.json.
func LoadMemoryCatalog(path string) (MemoryCatalog, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a test helper; the path comes from the caller
	if err != nil {
		return nil, fmt.Errorf("asset catalog: %w", err)
	}
	return ParseMemoryCatalog(data)
}
