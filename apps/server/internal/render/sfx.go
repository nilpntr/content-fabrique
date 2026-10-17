package render

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Generated sound effects (P3-10 part B, redesigned in P3-11): eleven categories with three or four variants each, synthesised in
// ffmpeg from aevalsrc and seeded anoisesrc with filters and envelopes (sfx_recipes.go, sfx_dsp.go and docs/design/audio-v1.md
// hold the design). There are no asset files and the seeds are fixed, so the samples are the same on every render and every
// machine (to the precision of the float maths of ffmpeg's expression evaluator), and they are ours: the export is "licensed"
// and nothing is cue-only about them (D-03 is about the trend sounds we do not own).
//
// Every number written into the graph is a constant of the recipes, a number the compiler formatted (the start and the gain of
// an item, the jitter taken from a hash of its id), and the effect is chosen by a switch on the category and variant enums, so
// nothing from the spec is written into the graph as text.
//
// Level: each effect is loudness-matched to its category (sfxLevelDB), then goes through sfxBaseDB and the item's gain_db. The
// effects are mixed in after the loudness normalisation of the programme (the way the preview's beat guide is), at that fixed
// level, followed by a limiter: a lone impact on a silent timeline is not boosted to -14 LUFS, and an effect on top of a
// normalised programme cannot push the mix over its peak ceiling.
//
// The limiter is a sample-peak limiter at sfxLimitDB (-1.5 dBFS), not a true-peak limiter: the 0.5 dB between it and the -1 dBTP
// target is headroom for the inter-sample peaks that the AAC encoder and a player's resampler add (a sample peak of -1.5 dBFS
// stays under -1 dBTP in practice; TestSoundEffectsLeaveTheProgrammeAlone measures the true peak with ebur128). Its look-ahead
// delay is compensated by alimiter itself (latency=1), which also flushes the look-ahead buffer at the end of the stream, so
// the sound stays where it was and keeps its length; the same test pins that with an impulse.
const (
	sfxBaseDB = -6
	// sfxLimitDB is the peak ceiling of the mix in dBFS.
	sfxLimitDB = -1.5
)

// sfxLimiter is the limiter stage that follows the mix of the effects (auto level off: it would normalise the mix). The ceiling is
// sfxLimitDB as the linear value alimiter takes (0.841395), and the attack is written out so that no default can change it.
var sfxLimiter = "alimiter=limit=" + f(math.Pow(10, sfxLimitDB/20)) + ":attack=5:release=50:level=false:latency=1"

// sfxSpecError is the error for an item whose category or variant the renderer does not know.
func sfxSpecError(it *timeline.SFX) error {
	return fmt.Errorf("%w: audio item %s: %q is not a sound effect", ErrInvalidSpec, it.ID, it.Sfx)
}

// builtSFX is one effect resolved and built: the statements that make it (to be placed on the timeline by the caller) and its
// length.
type builtSFX struct {
	stmts []string
	out   string
	ms    int64
	cat   string
	vari  string
}

// buildItem resolves an item's variant and builds its layers under the label prefix.
func buildItem(prefix string, it *timeline.SFX) (builtSFX, error) {
	variant, ok := timeline.ResolveSFXVariant(it.Sfx, it.Variant, it.ID)
	if !ok {
		if _, known := timeline.SFXVariants(it.Sfx); known {
			return builtSFX{}, fmt.Errorf("%w: audio item %s: %q is not a variant of %s", ErrInvalidSpec, it.ID, it.Variant, it.Sfx)
		}
		return builtSFX{}, sfxSpecError(it)
	}
	res, ok := buildSFX(prefix, it.Sfx, variant, it.ID)
	if !ok {
		return builtSFX{}, sfxSpecError(it)
	}
	return builtSFX{stmts: res.stmts, out: res.out, ms: res.ms, cat: it.Sfx, vari: variant}, nil
}

// sfxPlacement is one effect placed on the timeline.
type sfxPlacement struct {
	stmts    []string
	pad      string
	atMs     int64
	durMs    int64
	targetDB float64 // the category's level: what decides whether it ducks the programme
}

// sfxPlacements builds every effect of the spec in order and returns the first error.
func (c *compiler) sfxPlacements() ([]sfxPlacement, error) {
	var out []sfxPlacement
	n := 0
	for _, t := range c.spec.Tracks {
		tr, ok := t.(*timeline.AudioTrack)
		if !ok {
			continue
		}
		for _, item := range tr.Items {
			sfx, ok := item.(*timeline.SFX)
			if !ok {
				continue
			}
			if sfx.Export != timeline.ExportLicensed {
				return nil, fmt.Errorf("%w: audio item %s: export %q does not fit kind %q", ErrInvalidSpec, sfx.ID, sfx.Export, sfx.AudioKind())
			}
			it, err := buildItem("fx"+strconv.Itoa(n), sfx)
			if err != nil {
				return nil, err
			}
			gain := sfx.GainDB
			if gain != nil && (*gain < -30 || *gain > 6 || *gain != *gain) {
				return nil, fmt.Errorf("%w: audio item %s: gain %s dB is outside -30 to 6", ErrInvalidSpec, sfx.ID, num(*gain))
			}
			if sfx.AtMs < 0 || sfx.AtMs >= c.totalMs {
				continue
			}
			dur := min(it.ms, c.totalMs-sfx.AtMs)
			db := float64(sfxBaseDB)
			if gain != nil {
				db += *gain
			}
			tail := []string{stereo48, "volume=" + f(db) + "dB", "atrim=end=" + secs(dur), "asetpts=PTS-STARTPTS"}
			if dur < it.ms { // cut by the end of the timeline: a short fade instead of a click
				fade := min(dur, 10)
				tail = append(tail, fmt.Sprintf("afade=t=out:st=%s:d=%s", secs(dur-fade), secs(fade)))
			}
			if sfx.AtMs > 0 {
				tail = append(tail, fmt.Sprintf("adelay=%d:all=1", sfx.AtMs))
			}
			pad := fmt.Sprintf("fx%d", n)
			n++
			st := append([]string{}, it.stmts...)
			st = append(st, chain([]string{it.out}, tail, pad))
			out = append(out, sfxPlacement{stmts: st, pad: pad, atMs: sfx.AtMs, durMs: dur, targetDB: sfxTargetDB[it.cat]})
		}
	}
	return out, nil
}

// SFXPreviewGraph returns the filter graph (one statement per element, ending in [aout]) of one effect rendered alone, the way it
// is mixed into a render: stereo at 48 kHz, at sfxBaseDB plus gainDB, then the limiter, starting at time 0. id drives the jitter
// and the "auto" variant. It also returns the effect's length in ms. It is for the gallery (cfctl sfx gallery) and the tests.
func SFXPreviewGraph(category, variant, id string, gainDB float64) (graph string, ms int64, err error) {
	it, err := buildItem("fx0", &timeline.SFX{ID: id, Sfx: category, Variant: variant, Export: timeline.ExportLicensed})
	if err != nil {
		return "", 0, err
	}
	if gainDB != gainDB || gainDB < -30 || gainDB > 6 {
		return "", 0, fmt.Errorf("%w: gain %s dB is outside -30 to 6", ErrInvalidSpec, num(gainDB))
	}
	tail := []string{stereo48, "volume=" + f(sfxBaseDB+gainDB) + "dB", sfxLimiter}
	st := append([]string{}, it.stmts...)
	st = append(st, chain([]string{it.out}, tail, "aout"))
	return joinStmts(st), it.ms, nil
}

// SFXRawGraph is SFXPreviewGraph without the base level and the limiter: the effect as the recipe makes it, at its matched
// level, mono or stereo as built. The tests measure the design with it.
func SFXRawGraph(category, variant, id string) (graph string, ms int64, err error) {
	it, err := buildItem("fx0", &timeline.SFX{ID: id, Sfx: category, Variant: variant, Export: timeline.ExportLicensed})
	if err != nil {
		return "", 0, err
	}
	st := append([]string{}, it.stmts...)
	st = append(st, chain([]string{it.out}, []string{stereo48}, "aout"))
	return joinStmts(st), it.ms, nil
}

func joinStmts(st []string) string {
	out := ""
	for i, s := range st {
		if i > 0 {
			out += ";"
		}
		out += s
	}
	return out
}

// The ducking of the programme under the effects (P3-11). Where an effect overlaps speech from a clip's own sound, the programme is
// compressed a few dB by a sidechain compressor keyed on the effect, so that a hit does not bury a word: the gain reduction follows
// the effect's own level (a riser ducks more at its peak than at its start), it is the same on every render (no randomness, no
// look at the audio), and with no overlap the programme is not touched at all. Earshot's rule is 3 to 6 dB under voice
// (launch-video-sound.md section 9); the threshold and ratio below give 3 to 5 dB for a hit at its default level and at most 6 dB
// at +6 dB of gain.
const (
	duckThreshold = "0.05" // -26 dBFS: about 20 dB under a hit's peak at the default level
	duckRatio     = "1.25" // 20 dB over the threshold is a reduction of 4 dB
	duckAttackMs  = 8
	duckReleaseMs = 200
	// speechGapMs is the pause between two words under which they are one stretch of speech.
	speechGapMs = 250
	// duckMinTargetDB is the quietest category that ducks: the accents (impact, sting, sub drop, riser, downlifter, whoosh, glitch),
	// not the detail (swish, shimmer, pop, click), which are too small to cover a word.
	duckMinTargetDB = -13
	// duckMinClipGainDB is the gain of a clip's own sound at or under which it is taken for muted: no speech to cover.
	duckMinClipGainDB = -24
)

// speechSpans returns where, in output ms, a clip's own sound carries speech: the transcript words of every clip whose source audio
// is in the mix (a clip with a speed ramp plays none, see audioGraph), merged when the pause between two words is short.
func (c *compiler) speechSpans() [][2]int64 {
	var spans [][2]int64
	for _, w := range c.mapWords() {
		cl := c.clips[w.clip]
		a := cl.item.Audio
		if a == nil || !a.UseSourceOrDefault() || !cl.asset.HasAudio || cl.item.HasRamp() || a.GainDBOrDefault() <= duckMinClipGainDB {
			continue
		}
		start, end := timeline.FrameStartMs(w.startF, c.fps), timeline.FrameStartMs(w.endF, c.fps)
		if n := len(spans); n > 0 && start-spans[n-1][1] <= speechGapMs {
			spans[n-1][1] = max(spans[n-1][1], end)
			continue
		}
		spans = append(spans, [2]int64{start, end})
	}
	return spans
}

// duck returns the statements that duck the programme (the pad prog) under the effects of places that overlap speech, the pad of
// the ducked programme and the pads of the effects for the final mix (the ones that feed the key are split first: a pad can be read
// once). With nothing to duck it returns no statements, prog and pads as they were.
func (c *compiler) duck(prog string, places []sfxPlacement, pads []string) (stmts []string, out string, newPads []string) {
	spans := c.speechSpans()
	if len(spans) == 0 {
		return nil, prog, pads
	}
	newPads = slices.Clone(pads)
	var keys []string
	for i, p := range places {
		if p.targetDB < duckMinTargetDB {
			continue
		}
		overlaps := false
		for _, s := range spans {
			if s[0] < p.atMs+p.durMs && s[1] > p.atMs {
				overlaps = true
				break
			}
		}
		if !overlaps {
			continue
		}
		mixCopy, keyCopy := p.pad+"m", p.pad+"k"
		stmts = append(stmts, fmt.Sprintf("[%s]asplit=2[%s][%s]", p.pad, mixCopy, keyCopy))
		newPads[i] = mixCopy
		keys = append(keys, keyCopy)
	}
	if len(keys) == 0 {
		return nil, prog, pads
	}
	// The key is the sum of those effects, as long as the programme (the compressor ends with its shortest input otherwise).
	var in string
	for _, k := range keys {
		in += "[" + k + "]"
	}
	keyStmt := in
	if len(keys) > 1 {
		keyStmt += fmt.Sprintf("amix=inputs=%d:duration=longest:normalize=0:dropout_transition=0,", len(keys))
	}
	keyStmt += fmt.Sprintf("apad=whole_dur=%s[fxkey]", secs(c.totalMs))
	stmts = append(stmts, keyStmt,
		fmt.Sprintf("[%s][fxkey]sidechaincompress=threshold=%s:ratio=%s:attack=%d:release=%d:makeup=1:knee=4:detection=peak[aduck]",
			prog, duckThreshold, duckRatio, duckAttackMs, duckReleaseMs))
	return stmts, "aduck", newPads
}
