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

// Asset is what Validate needs to know about one platform asset. The JSON tags match fixtures/assets.json.
type Asset struct {
	DurationMs int64     `json:"duration_ms"`
	Kind       AssetKind `json:"kind"`
	Rights     Rights    `json:"rights"`
}

// AssetCatalog is what Validate needs to know about the platform's assets. The server implements it over the assets
// table; tests use MemoryCatalog. Validate looks each asset up once per item.
type AssetCatalog interface {
	// Lookup returns the asset with the given id, and false if the asset is not known.
	Lookup(assetID string) (Asset, bool)
}

// MemoryCatalog is an in-memory AssetCatalog keyed by asset id.
type MemoryCatalog map[string]Asset

// Lookup implements AssetCatalog.
func (c MemoryCatalog) Lookup(assetID string) (Asset, bool) {
	a, ok := c[assetID]
	return a, ok
}

// ParseMemoryCatalog reads the format of fixtures/assets.json: {"assets": {"<asset id>": {"duration_ms", "kind",
// "rights"}}}. Other top-level keys (the file's description) are ignored.
func ParseMemoryCatalog(data []byte) (MemoryCatalog, error) {
	var f struct {
		Assets map[string]Asset `json:"assets"`
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
