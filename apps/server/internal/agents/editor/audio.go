package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The audio checks of check_edit (P3-12): the ears (internal/audiocheck) listen to the preview render of the version and the result
// is folded into the report as three checks, audio_level, audio_bed and audio_effects.
//
// Which sound is measured, and why not the video. A preview has a generated beat guide (a click on every beat) mixed in for the
// editor's own review; the export has none. The audio check must hear the export, so a preview with a guide also writes the mix
// WITHOUT the guide as a second output, the audio stem (storage.RenderAudioStemName), and the check measures that. The other way
// (measure the video and subtract the guide) would need the guide's exact samples and the loudness normalisation's gain, and any
// click under a hit would still bleed into the onset detector; the stem costs one more small output of the same ffmpeg run. A preview
// without a guide (no beat markers) has no stem and the video's own sound is measured.

// ErrNoAudioStem is what an AudioSampler returns for a preview that should have an audio stem (the timeline has a beat guide) and has
// none: the preview was rendered before the stem existed, and the video's sound has the guide in it.
var ErrNoAudioStem = errors.New("editor: the preview has no audio stem")

// AudioResult is the ears' report of a preview and how long listening took.
type AudioResult struct {
	Report audiocheck.Report
	// Elapsed is the time of the whole step: the download of the stem (or the video) and the ffmpeg run and analysis.
	Elapsed time.Duration
	// Source is what was measured: "stem" (the mix without the beat guide) or "preview" (the video's own sound).
	Source string
}

// AudioSampler listens to the preview render of a version. *RenderMeasurer is the real one.
type AudioSampler interface {
	// Audio measures the sound of the version's preview render, the profile chosen from the timeline (audiocheck.ProfileFromSpec).
	// ErrNoPreview when the version has no ready preview, ErrNoAudioStem when it has a beat guide and no stem.
	Audio(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, spec *timeline.Spec) (AudioResult, error)
}

var _ AudioSampler = (*RenderMeasurer)(nil)

const (
	maxStemBytes    = 64 << 20
	maxTimeMapBytes = 4 << 20
)

// hasBeatGuide says the preview of this timeline carries the generated beat guide (the renderer adds it when there are beat,
// downbeat or drop markers).
func hasBeatGuide(spec *timeline.Spec) bool {
	total := timeline.TotalDuration(spec)
	for _, m := range spec.Markers {
		if m.AtMs >= total {
			continue // the renderer puts no click at or past the end
		}
		switch m.Kind {
		case timeline.MarkerBeat, timeline.MarkerDownbeat, timeline.MarkerDrop:
			return true
		}
	}
	return false
}

// Audio implements AudioSampler.
func (m *RenderMeasurer) Audio(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, spec *timeline.Spec) (AudioResult, error) {
	start := time.Now()
	r, err := m.previewOf(ctx, ws, project, version)
	if err != nil {
		return AudioResult{}, err
	}
	want, err := storage.RenderKey(ws, project, r.ID, storage.RenderMP4Name)
	if err != nil || *r.Mp4Key != want {
		return AudioResult{}, fmt.Errorf("editor: render %s has a video key that is not its own", r.ID)
	}
	key, source := want, "preview"
	if hasBeatGuide(spec) {
		key, err = storage.RenderKey(ws, project, r.ID, storage.RenderAudioStemName)
		if err != nil {
			return AudioResult{}, err
		}
		source = "stem"
	}
	dir, err := os.MkdirTemp(m.TempDir, "audiocheck-")
	if err != nil {
		return AudioResult{}, fmt.Errorf("editor: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return AudioResult{}, err
	}
	local := filepath.Join(dir, "in", "audio")
	limit := int64(maxRenderBytes)
	if source == "stem" {
		limit = maxStemBytes
	}
	if err := m.fetch(ctx, key, local, limit); err != nil {
		if source == "stem" && errors.Is(err, storage.ErrNotFound) {
			return AudioResult{}, ErrNoAudioStem
		}
		return AudioResult{}, err
	}
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return AudioResult{}, err
	}
	rep, err := audiocheck.Check(ctx, m.Runner, local, m.timeMap(ctx, ws, project, r.ID), audiocheck.WithSpec(spec), audiocheck.WithWorkDir(out))
	if err != nil {
		return AudioResult{}, fmt.Errorf("editor: listen to the preview: %w", err)
	}
	return AudioResult{Report: rep, Elapsed: time.Since(start), Source: source}, nil
}

// timeMap reads the stored time map of the render (its cuts, for the sound-on-the-cuts check). It is optional: nil when it cannot be
// read.
func (m *RenderMeasurer) timeMap(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, rid ids.RenderID) []render.TimeMapEntry {
	key, err := storage.RenderKey(ws, project, rid, storage.RenderTimeMapName)
	if err != nil {
		return nil
	}
	obj, err := m.Objects.Get(ctx, key)
	if err != nil {
		return nil
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > maxTimeMapBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, maxTimeMapBytes+1))
	if err != nil || len(data) > maxTimeMapBytes {
		return nil
	}
	var tm []render.TimeMapEntry
	if json.Unmarshal(data, &tm) != nil {
		return nil
	}
	return tm
}

// ---- the three checks -------------------------------------------------------------------------------------------------

// The audio check ids.
const (
	CheckAudioLevel   = "audio_level"
	CheckAudioBed     = "audio_bed"
	CheckAudioEffects = "audio_effects"
)

// audioChecks folds the ears' checklist into check_edit's checks. The ears grade pass, warn (worth a look), fail and info; check_edit
// has pass, fail and skip, and the editor revises on a fail, so a warn stays a pass whose line says "borderline" (an estimate that is
// close to its bar is not a reason for the one revision pass), and an info is not shown. The details are the ears' own lines, each
// with its one-line fix.
func audioChecks(res AudioResult) []Check {
	r := res.Report
	groups := []struct {
		id  string
		ids []string
	}{
		{CheckAudioLevel, []string{"has_audio", "loudness", "true_peak", "clipping", "dc_offset", "channel_balance", "silent_gap"}},
		{CheckAudioBed, []string{"bed_present", "bed_gaps", "bed_steady"}},
		{CheckAudioEffects, []string{"sfx_sync", "sfx_cut_through", "trend_audible", "sfx_density"}},
	}
	byID := map[string]audiocheck.CheckItem{}
	for _, c := range r.Checklist {
		byID[c.ID] = c
	}
	var out []Check
	for _, g := range groups {
		var fails, borders, passes []string
		for _, id := range g.ids {
			c, ok := byID[id]
			if !ok || c.Status == audiocheck.StatusInfo {
				continue
			}
			line := fmt.Sprintf("%s %s (target %s)", lower1(c.Title), c.Value, c.Target)
			switch c.Status {
			case audiocheck.StatusFail:
				fails = append(fails, line+". "+c.Fix)
			case audiocheck.StatusWarn:
				borders = append(borders, line+". "+c.Fix)
			default:
				passes = append(passes, fmt.Sprintf("%s %s", lower1(c.Title), c.Value)) // a pass needs no target: the answer stays short
			}
		}
		if len(fails)+len(borders)+len(passes) == 0 {
			if g.id == CheckAudioBed && r.Profile != audiocheck.ProfileSoundReady {
				continue // the bed checks belong to the sound-ready profile
			}
			out = append(out, Check{ID: g.id, Status: CheckSkip, Detail: "nothing to measure in this sound (" + r.Profile + " profile)"})
			continue
		}
		switch {
		case len(fails) > 0:
			out = append(out, Check{ID: g.id, Status: CheckFail, Detail: joinSentences(fails) + borderNote(borders)})
		case len(borders) > 0:
			out = append(out, Check{ID: g.id, Status: CheckPass, Detail: "borderline, not worth a revision: " + joinSentences(borders)})
		default:
			out = append(out, Check{ID: g.id, Status: CheckPass, Detail: joinSentences(passes)})
		}
	}
	return out
}

func borderNote(b []string) string {
	if len(b) == 0 {
		return ""
	}
	return " Also borderline: " + joinSentences(b)
}

func joinSentences(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += "; "
		}
		out += x
	}
	return out
}

func lower1(s string) string {
	if s == "" {
		return s
	}
	b := []rune(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

// audioSkipped is the check that stands for the audio checks when they could not run.
func audioSkipped(reason string) []Check {
	return []Check{
		{ID: CheckAudioLevel, Status: CheckSkip, Detail: reason},
		{ID: CheckAudioBed, Status: CheckSkip, Detail: reason},
		{ID: CheckAudioEffects, Status: CheckSkip, Detail: reason},
	}
}
