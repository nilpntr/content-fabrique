package analysis

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// The series as stored (asset_signals): one byte string per series, little-endian int16 for motion, sharp and scene and uint8 for
// luma, contrast and high, each with a scale (value = stored / scale), which is 9 bytes a sample: 8 KB for a 90 s clip,
// 160 KB for the longest accepted one (30 minutes). Values past what a type holds saturate.

// Scales of the packed series. Luma, contrast and high are whole grey levels.
var packScales = map[string]float64{"motion": 100, "sharp": 100, "scene": 10, "luma": 1, "contrast": 1, "high": 1}

// Pack encodes the series for storage.
func Pack(s Series) (store.PackedSeries, error) {
	scales, err := json.Marshal(struct {
		StepMs int                `json:"step_ms"`
		Scale  map[string]float64 `json:"scale"`
		Types  map[string]string  `json:"types"`
	}{s.StepMs, packScales, map[string]string{
		"motion": "int16le", "sharp": "int16le", "scene": "int16le", "luma": "uint8", "contrast": "uint8", "high": "uint8",
	}})
	if err != nil {
		return store.PackedSeries{}, err
	}
	return store.PackedSeries{
		Scales: scales,
		Motion: packInt16(s.Motion, packScales["motion"]), Sharp: packInt16(s.Sharp, packScales["sharp"]), Scene: packInt16(s.Scene, packScales["scene"]),
		Luma: packUint8(s.Luma), Contrast: packUint8(s.Contrast), High: packUint8(s.High),
	}, nil
}

// Unpack decodes a stored series (the tests and tools that read an analysis back).
func Unpack(p store.PackedSeries, stepMs int) (Series, error) {
	n := len(p.Luma)
	if len(p.Contrast) != n || len(p.High) != n || len(p.Motion) != 2*n || len(p.Sharp) != 2*n || len(p.Scene) != 2*n {
		return Series{}, fmt.Errorf("analysis: the packed series do not have the same length")
	}
	return Series{
		StepMs: stepMs,
		Motion: unpackInt16(p.Motion, packScales["motion"]), Sharp: unpackInt16(p.Sharp, packScales["sharp"]), Scene: unpackInt16(p.Scene, packScales["scene"]),
		Luma: unpackUint8(p.Luma), Contrast: unpackUint8(p.Contrast), High: unpackUint8(p.High),
	}, nil
}

func packInt16(x []float64, scale float64) []byte {
	out := make([]byte, 2*len(x))
	for i, v := range x {
		q := math.Round(v * scale)
		q = math.Max(math.MinInt16, math.Min(math.MaxInt16, q))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(q))) //nolint:gosec // clamped to the int16 range above
	}
	return out
}

func unpackInt16(b []byte, scale float64) []float64 {
	out := make([]float64, len(b)/2)
	for i := range out {
		out[i] = float64(int16(binary.LittleEndian.Uint16(b[2*i:]))) / scale //nolint:gosec // reinterpreting the stored two's complement
	}
	return out
}

func packUint8(x []float64) []byte {
	out := make([]byte, len(x))
	for i, v := range x {
		out[i] = byte(math.Max(0, math.Min(255, math.Round(v)))) //nolint:gosec // clamped to 0..255
	}
	return out
}

func unpackUint8(b []byte) []float64 {
	out := make([]float64, len(b))
	for i, v := range b {
		out[i] = float64(v)
	}
	return out
}
