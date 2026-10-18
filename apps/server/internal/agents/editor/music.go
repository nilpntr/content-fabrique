package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/music"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The song of an edit (P6-06, D-34): the creator gives the edit a song (an audio clip of the project, or the sound of one of their
// videos), the analyze job measures it once (package music), the editor reads the analysis (a line in the footage list, get_music for the
// times) and cuts to it, and check_edit holds the cuts and the hook to it.

// MusicReader reads the music analyses. *store.Store is the real one.
type MusicReader interface {
	ListAssetMusic(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.AssetMusicRow, error)
}

// LoadMusic reads the music analyses of the assets (by asset id); an asset with none, or one of an older version, is left out.
func LoadMusic(ctx context.Context, src MusicReader, ws ids.WorkspaceID, assets []ids.AssetID) (map[string]music.Analysis, error) {
	if src == nil || len(assets) == 0 {
		return nil, nil
	}
	rows, err := src.ListAssetMusic(ctx, ws, assets)
	if err != nil {
		return nil, fmt.Errorf("read the music analyses: %w", err)
	}
	out := make(map[string]music.Analysis, len(rows))
	for _, r := range rows {
		var a music.Analysis
		if json.Unmarshal(r.Analysis, &a) == nil && a.Version == music.Version {
			out[r.AssetID.String()] = a
		}
	}
	return out, nil
}

// TimelineMusic is the song of an edit on the timeline: its beats, downbeats, phrases and strong onsets, and its drop, in output
// milliseconds (the part of the song the item plays: source time minus source_offset_ms plus at_ms).
type TimelineMusic struct {
	ItemID, AssetID string
	TempoBPM        float64
	BeatsMs         []int64
	DownbeatsMs     []int64
	PhrasesMs       []int64
	OnsetsMs        []int64
	// DropMs is the drop on the timeline; HasDrop is false when the song has none or it is outside the part that plays.
	DropMs  int64
	HasDrop bool
	// SourceOffsetMs is the item's source_offset_ms, and SourceDropMs the drop in the song's own time (for the fix that moves the song).
	SourceOffsetMs, SourceDropMs int64
}

// MusicOnTimeline maps the analysis of the spec's first song with an analysis onto the timeline; nil when the spec has no song or none
// was analysed.
func MusicOnTimeline(spec *timeline.Spec, analyses map[string]music.Analysis) *TimelineMusic {
	total := timeline.TotalDuration(spec)
	for _, it := range timeline.MusicItems(spec) {
		assetID, _ := it.AssetRef()
		a, ok := analyses[assetID]
		if !ok || !a.Music {
			continue
		}
		com := it.Common()
		dur := min(com.EffectiveDurationMs(total), total-com.AtMs)
		from, to := com.SourceOffsetMs, com.SourceOffsetMs+dur
		mapOut := func(list []int64) []int64 {
			var out []int64
			for _, t := range music.Between(list, from, to) {
				out = append(out, t-from+com.AtMs)
			}
			return out
		}
		tm := &TimelineMusic{
			ItemID: com.ID, AssetID: assetID, TempoBPM: a.TempoBPM, SourceOffsetMs: com.SourceOffsetMs, SourceDropMs: a.DropMs,
			BeatsMs: mapOut(a.BeatsMs), DownbeatsMs: mapOut(a.DownbeatsMs), PhrasesMs: mapOut(a.PhrasesMs), OnsetsMs: mapOut(a.OnsetsMs),
		}
		if a.DropMs > 0 && a.DropMs >= from && a.DropMs < to {
			tm.DropMs, tm.HasDrop = a.DropMs-from+com.AtMs, true
		}
		return tm
	}
	return nil
}

// hits are the times a cut may land on: the beats and the strong onsets, sorted.
func (m *TimelineMusic) hits() []int64 {
	out := slices.Concat(m.BeatsMs, m.OnsetsMs)
	slices.Sort(out)
	return slices.Compact(out)
}

// isSong says whether a clip of the project is a song the editor may cut to: an audio file the creator gave (asset kind audio), or a video
// whose sound the creator chose for this edit (chosen: the assets of the creator_audio items on the project's timeline). A footage clip's
// own sound is never a song, even when the analysis heard music in it: a gym plays music, and the camera picks it up under the clatter.
func isSong(r store.ProjectFootageRow, chosen map[string]bool) bool {
	if r.HasAudio == nil || !*r.HasAudio {
		return false
	}
	return r.Kind == store.AssetKindAudio || chosen[r.AssetID.String()]
}

// chosenSounds are the assets of the creator_audio items on a timeline: a sound already chosen for the edit (by the creator, or kept from
// an earlier version), so a video named there is a song.
func chosenSounds(spec *timeline.Spec) map[string]bool {
	out := map[string]bool{}
	if spec == nil {
		return out
	}
	for _, it := range timeline.MusicItems(spec) {
		if ref, ok := it.AssetRef(); ok {
			out[ref] = true
		}
	}
	return out
}

// chosenSoundsOf reads the project's current timeline for chosenSounds; a project without one, or a read that fails, has none.
func (d Deps) chosenSoundsOf(ctx context.Context, ws ids.WorkspaceID, pid ids.ProjectID) map[string]bool {
	if d.Projects == nil {
		return nil
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil || det.Project.CurrentTimelineVersion == nil {
		return nil
	}
	vd, perr := d.Projects.Version(ctx, actor(ws), pid, int(*det.Project.CurrentTimelineVersion))
	if perr != nil {
		return nil
	}
	spec, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		return nil
	}
	return chosenSounds(spec)
}

// musicOf reads the music analyses of the project's songs (isSong), for the footage list and get_music. The analysis of a footage clip's
// own sound is not read: it is not a song.
func (d Deps) musicOf(ctx context.Context, ws ids.WorkspaceID, rows []store.ProjectFootageRow, chosen map[string]bool) map[string]music.Analysis {
	mr, ok := d.Store.(MusicReader)
	if !ok {
		return nil
	}
	var assets []ids.AssetID
	for _, r := range rows {
		if isSong(r, chosen) {
			assets = append(assets, r.AssetID)
		}
	}
	got, err := LoadMusic(ctx, mr, ws, assets)
	if err != nil {
		slog.WarnContext(ctx, "editor: the music analyses could not be read; the footage list has no music lines", "error", err)
		return nil
	}
	return got
}

// ---- get_music --------------------------------------------------------------------------------------------------

type getMusicArgs struct {
	AssetID string `json:"asset_id"`
	FromMs  *int64 `json:"from_ms,omitempty"`
	ToMs    *int64 `json:"to_ms,omitempty"`
}

// maxMusicWindowMs is the most of a song get_music lists at once.
const maxMusicWindowMs = 90_000

func (d Deps) getMusic(ctx context.Context, rc runtime.RunContext, in getMusicArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	rows, err := d.Store.ListProjectFootage(ctx, ws, pid)
	if err != nil {
		return runtime.Result{}, fmt.Errorf("list footage: %w", err)
	}
	i := slices.IndexFunc(rows, func(r store.ProjectFootageRow) bool { return r.AssetID.String() == in.AssetID })
	if i < 0 {
		return runtime.Result{}, runtime.UserErrorf("%q is not a clip of this project; use an id from list_footage", in.AssetID)
	}
	chosen := d.chosenSoundsOf(ctx, ws, pid)
	if !isSong(rows[i], chosen) && rows[i].Kind != store.AssetKindAudio {
		return runtime.Result{}, runtime.UserErrorf("clip %s is footage: its sound is what the camera heard (the gym's own music, the room), not a song the creator gave. A song is an audio file of the project (a clip with a song line in list_footage), or the sound of a video the creator chose for this edit. Without a song the edit stays sound-ready", in.AssetID)
	}
	an := d.musicOf(ctx, ws, rows[i:i+1], chosen)
	a, ok := an[in.AssetID]
	if !ok {
		return runtime.Result{}, runtime.UserErrorf("clip %s has no music analysis: it has no sound, its sound is not music, or it was not analysed yet. Without a song the edit stays sound-ready (the creator adds a sound in TikTok)", in.AssetID)
	}
	from, to := int64(0), a.DurationMs
	if in.FromMs != nil {
		from = max(*in.FromMs, 0)
	}
	if in.ToMs != nil {
		to = min(*in.ToMs, a.DurationMs)
	}
	if to <= from {
		return runtime.Result{}, runtime.UserErrorf("to_ms must be after from_ms, inside the song (0 to %d ms)", a.DurationMs)
	}
	cut := ""
	if to-from > maxMusicWindowMs {
		to, cut = from+maxMusicWindowMs, fmt.Sprintf(" (cut to %d s: ask for from_ms %d for the rest)", maxMusicWindowMs/1000, from+maxMusicWindowMs)
	}
	return runtime.Text(MusicText(in.AssetID, a, from, to) + cut), nil
}

// MusicText is get_music's answer: the song's numbers between from and to (its own time), and how to use them.
func MusicText(assetID string, a music.Analysis, from, to int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Song %s, %d to %d ms of its own time. %s.\n", assetID, from, to, a.Line())
	list := func(name string, l []int64) {
		w := music.Between(l, from, to)
		fmt.Fprintf(&b, "%s (%d): %s\n", name, len(w), joinMs(w))
	}
	list("beats_ms", a.BeatsMs)
	list("downbeats_ms", a.DownbeatsMs)
	list("phrases_ms (a phrase is 4 bars)", a.PhrasesMs)
	list("strong onsets_ms", a.OnsetsMs)
	if a.DropMs > 0 {
		fmt.Fprintf(&b, "drop_ms: %d\n", a.DropMs)
	}
	b.WriteString("These times are in the song's own time. On the timeline a time t of the song plays at at_ms + t - source_offset_ms of its creator_audio item. " +
		"To put the drop on the end of the hook, set source_offset_ms = drop_ms - (the time of the first cut), or make the hook as long as the drop. " +
		"To cut on the music, give snap_cuts_to_beats grid {\"beats_ms\": <these beats_ms>, \"downbeats_ms\": <these downbeats_ms>} with sound_id the song's item id (the grid is moved with the item), max_shift_ms 80 and write_markers true.")
	return b.String()
}

func joinMs(l []int64) string {
	if len(l) == 0 {
		return "none"
	}
	parts := make([]string, len(l))
	for i, v := range l {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}
