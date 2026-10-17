package render

// sfxLevelDB is the loudness match: the correction, in dB, that brings every variant to its category's target (sfxTargetDB), measured
// without jitter. It is a table of measurements, not of taste: when a recipe changes, run
//
//	go test ./internal/render -run TestSFXLoudnessMatching -sfx-calibrate -v
//
// and paste the table it prints. TestSFXLoudnessMatching fails when a variant is more than 1 dB off its target.
var sfxLevelDB = map[string]float64{
	"click/mech":       -3.8,
	"click/soft":       -5.9,
	"click/tonal":      -6.7,
	"click/ui":         0.1,
	"downlifter/fall":  1.2,
	"downlifter/short": 0.6,
	"downlifter/tape":  -3.3,
	"glitch/chirp":     -0.5,
	"glitch/crush":     3.2,
	"glitch/stutter":   -1.5,
	"impact/crack":     4.2,
	"impact/deep":      -3.3,
	"impact/metal":     0.2,
	"impact/punch":     -1.2,
	"pop/bubble":       -7.1,
	"pop/double":       -7.7,
	"pop/tick":         -3.6,
	"riser/long":       5.2,
	"riser/pitch":      -2.0,
	"riser/sweep":      -1.0,
	"riser/tension":    6.6,
	"shimmer/rise":     -9.1,
	"shimmer/sparkle":  -12.5,
	"shimmer/wash":     -7.5,
	"sting/bright":     -1.5,
	"sting/dark":       -2.6,
	"sting/hit":        -2.8,
	"sub_drop/boom":    -1.8,
	"sub_drop/long":    -2.1,
	"sub_drop/quick":   -0.7,
	"swish/blade":      1.0,
	"swish/cloth":      1.9,
	"swish/thin":       0.5,
	"whoosh/air":       1.7,
	"whoosh/fast":      1.8,
	"whoosh/heavy":     4.7,
}
