package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const (
	assetA     = "ast_01j3er3kwcgrasdgd4aznmxxe0" // video, 60 s
	assetB     = "ast_01jex6xx15ayjx8epfhfjj9s11" // video, 45 s
	assetMusic = "ast_01j0h9rew727hk2pt0v5b31h44" // audio, licensed
	trendID    = "trd_01jcmevy4tr3fffvjc82eh7rjm"
)

func ptr[T any](v T) *T { return &v }

// clipAt builds a clip of the source range [in, out) at the given timeline position.
func clipAt(id, asset string, in, out, at int64, opts ...func(*timeline.VideoItem)) timeline.VideoItem {
	it := timeline.VideoItem{ID: id, AssetID: asset, Source: timeline.Source{InMs: in, OutMs: out}, AtMs: at}
	for _, o := range opts {
		o(&it)
	}
	return it
}

func withTransition(typ string, ms int64) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.TransitionIn = &timeline.Transition{Type: typ, DurationMs: ms} }
}

func withSpeed(s float64) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.Speed = &s }
}

func withSourceAudio(gain float64) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.Audio = &timeline.VideoAudio{UseSource: ptr(true), GainDB: ptr(gain)} }
}

// specOf builds a 9:16 spec from main items and further tracks.
func specOf(fps int, items []timeline.VideoItem, tracks ...timeline.Track) *timeline.Spec {
	return &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: fps, Background: "#000000"},
		Tracks: append([]timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID, Items: items}}, tracks...),
	}
}

func plainResolver() AssetResolver {
	return ResolverFunc(func(id string, v Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/a/" + string(v) + "/" + id, HasAudio: true}, nil
	})
}

func mustCompile(t *testing.T, spec *timeline.Spec, prof Profile) Plan {
	t.Helper()
	plan, err := Compile(spec, prof, plainResolver())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestCompileIsDeterministic(t *testing.T) {
	cat := testAssets(t)
	for _, name := range renderFixtures(t) {
		spec := loadFixture(t, name, cat)
		for _, prof := range Profiles() {
			a, err := Compile(spec, prof, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			b, err := Compile(spec, prof, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s/%s: two compiles differ", name, prof.Name)
			}
		}
	}
}

// Compile must not depend on the order in which the main track lists its items: it sorts by at_ms.
func TestCompileSortsMainTrack(t *testing.T) {
	in := []timeline.VideoItem{clipAt("c1", assetA, 0, 1000, 0), clipAt("c2", assetB, 0, 1000, 1000)}
	rev := []timeline.VideoItem{in[1], in[0]}
	a := mustCompile(t, specOf(30, in), Preview)
	b := mustCompile(t, specOf(30, rev), Preview)
	if !reflect.DeepEqual(a, b) {
		t.Error("a main track listed out of order compiles differently")
	}
}

// Item boundaries are snapped from the cumulative at_ms, never from summed durations: thirty clips of 1001 ms at 30 fps
// are 30.03 frames each, and the total must be Frame(30030) = 901, not 30 x 30.
func TestFrameSnappingDoesNotDrift(t *testing.T) {
	var items []timeline.VideoItem
	for i := range 30 {
		items = append(items, clipAt(fmt.Sprintf("c%d", i+1), assetA, 0, 1001, int64(i)*1001))
	}
	plan := mustCompile(t, specOf(30, items), Preview)
	if want := timeline.Frame(30*1001, 30); plan.TotalFrames != want {
		t.Fatalf("total frames %d, want %d", plan.TotalFrames, want)
	}
	if len(plan.CutList) != 29 {
		t.Fatalf("cut list has %d cuts, want 29", len(plan.CutList))
	}
	for i, c := range plan.CutList {
		if want := timeline.Frame(int64(i+1)*1001, 30); c.Frame != want {
			t.Errorf("cut %d at frame %d, want %d", i+1, c.Frame, want)
		}
		if want := timeline.FrameStartMs(c.Frame, 30); c.AtMs != want {
			t.Errorf("cut %d at %d ms, want %d", i+1, c.AtMs, want)
		}
	}
	// Every clip's frame count comes from its own boundaries, and they add up to the total.
	var sum int64
	for i, tm := range plan.TimeMap {
		sum += timeline.Frame(tm.OutEndMs, 30) - timeline.Frame(tm.OutStartMs, 30)
		if i > 0 && tm.OutStartMs != plan.TimeMap[i-1].OutEndMs {
			t.Errorf("time map entry %d does not start where the previous one ends", i)
		}
	}
	if sum != plan.TotalFrames {
		t.Errorf("the time map covers %d frames, the plan has %d", sum, plan.TotalFrames)
	}
}

func TestFrameRates(t *testing.T) {
	for _, fps := range []int{24, 25, 30, 60} {
		items := []timeline.VideoItem{
			clipAt("c1", assetA, 0, 2000, 0),
			clipAt("c2", assetB, 0, 2000, 1700, withTransition(timeline.TransitionCrossfade, 300)),
			clipAt("c3", assetA, 5000, 7000, 3400, withTransition(timeline.TransitionWhip, 300)),
		}
		plan := mustCompile(t, specOf(fps, items), Final)
		if want := timeline.Frame(5400, fps); plan.TotalFrames != want {
			t.Errorf("%d fps: total %d frames, want %d", fps, plan.TotalFrames, want)
		}
		wantOverlap := []int64{timeline.Frame(2000, fps) - timeline.Frame(1700, fps), timeline.Frame(3700, fps) - timeline.Frame(3400, fps)}
		for i, c := range plan.CutList {
			if c.TransitionFrames < 1 || c.TransitionFrames != wantOverlap[i] {
				t.Errorf("%d fps: cut %s overlaps %d frames, want %d", fps, c.ItemID, c.TransitionFrames, wantOverlap[i])
			}
		}
		if !strings.Contains(plan.FilterComplex, fmt.Sprintf("fps=%d", fps)) {
			t.Errorf("%d fps: the graph never sets the frame rate", fps)
		}
	}
}

func TestXfadeOffsetsComeFromTheCutList(t *testing.T) {
	items := []timeline.VideoItem{
		clipAt("c1", assetA, 0, 3000, 0),
		clipAt("c2", assetB, 0, 3000, 2600, withTransition(timeline.TransitionCrossfade, 400)),
		clipAt("c3", assetA, 0, 3000, 5200, withTransition(timeline.TransitionDipToBlack, 400)),
	}
	plan := mustCompile(t, specOf(30, items), Final)
	for _, want := range []string{
		"xfade=transition=fade:duration=0.400000:offset=2.600000",
		"xfade=transition=fadeblack:duration=0.400000:offset=5.200000",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Errorf("graph lacks %q:\n%s", want, PrettyFilter(plan.FilterComplex))
		}
	}
	if plan.CutList[0].TransitionFrames != 12 || plan.CutList[0].Transition != timeline.TransitionCrossfade {
		t.Errorf("cut list %+v", plan.CutList)
	}
	if plan.TotalFrames != 30*(3000+3000+3000-800)/1000 {
		t.Errorf("total frames %d", plan.TotalFrames)
	}
}

// A mix of cuts and transitions joins with concat for the cuts and xfade for the transitions; all cuts is one concat.
func TestJoinStrategies(t *testing.T) {
	cuts := mustCompile(t, specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 1000, 0), clipAt("c2", assetA, 0, 1000, 1000), clipAt("c3", assetA, 0, 1000, 2000)}), Final)
	if !strings.Contains(cuts.FilterComplex, "[v0][v1][v2]concat=n=3:v=1:a=0[vout]") || strings.Contains(cuts.FilterComplex, "xfade") {
		t.Errorf("all cuts: %s", PrettyFilter(cuts.FilterComplex))
	}
	mixed := mustCompile(t, specOf(30, []timeline.VideoItem{
		clipAt("c1", assetA, 0, 1000, 0),
		clipAt("c2", assetA, 0, 1000, 1000),
		clipAt("c3", assetA, 0, 1000, 1700, withTransition(timeline.TransitionCrossfade, 300)),
	}), Final)
	if !strings.Contains(mixed.FilterComplex, "[v0][v1]concat=n=2:v=1:a=0[j1]") || !strings.Contains(mixed.FilterComplex, "[j1][v2]xfade=") {
		t.Errorf("mixed: %s", PrettyFilter(mixed.FilterComplex))
	}
	single := mustCompile(t, specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 1000, 0)}), Final)
	if !strings.Contains(single.FilterComplex, "[v0]null[vout]") {
		t.Errorf("single: %s", PrettyFilter(single.FilterComplex))
	}
}

func TestProfiles(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0)})
	spec.Output = timeline.Output{Format: timeline.Format4x5, Width: 1080, Height: 1350, FPS: 30, Background: "#102030"}

	pre := mustCompile(t, spec, Preview)
	// Half of 1080x1350 is 540x675; yuv420p needs even sizes, so the height rounds up to 676.
	if pre.OutputSize != (Size{Width: 540, Height: 676}) {
		t.Errorf("preview size %+v", pre.OutputSize)
	}
	fin := mustCompile(t, spec, Final)
	if fin.OutputSize != (Size{Width: 1080, Height: 1350}) {
		t.Errorf("final size %+v", fin.OutputSize)
	}
	joined := func(p Plan) string { return strings.Join(p.Args, " ") }
	for _, want := range []string{"-preset veryfast", "-crf 28", "-b:a 128k", "/a/proxy/" + assetA, "-map_metadata -1", "-fflags +bitexact"} {
		if !strings.Contains(joined(pre), want) {
			t.Errorf("preview args lack %q", want)
		}
	}
	if strings.Contains(joined(pre), "faststart") || strings.Contains(joined(pre), "-profile:v") {
		t.Errorf("preview args carry final-only flags: %s", joined(pre))
	}
	for _, want := range []string{"-preset medium", "-crf 18", "-profile:v high", "-b:a 192k", "-ar 48000", "-movflags +faststart", "/a/original/" + assetA, "-map_metadata -1", "-fflags +bitexact", "-pix_fmt yuv420p"} {
		if !strings.Contains(joined(fin), want) {
			t.Errorf("final args lack %q", want)
		}
	}
	if pre.Subtitles == nil || !pre.Subtitles.Watermark || fin.Subtitles != nil {
		t.Error("only the preview profile draws a watermark (and a spec without text has no other subtitles)")
	}
	if !strings.Contains(pre.FilterComplex, "ass=filename=subs.ass:fontsdir=fonts") || strings.Contains(fin.FilterComplex, "ass=") {
		t.Error("the ass filter belongs in the preview graph only")
	}
	if _, err := ParseProfile("draft"); err == nil {
		t.Error("an unknown profile name must be an error")
	}
}

// Text and captions compile to one ASS script burned in by the ass filter after the clips are joined; the rest of the graph is
// the same with and without them.
func TestTextBecomesOneScript(t *testing.T) {
	base := []timeline.VideoItem{clipAt("c1", assetA, 0, 4000, 0)}
	plain := mustCompile(t, specOf(30, base), Final)

	withText := specOf(30, base,
		&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{
			{ID: "t1", AtMs: 500, DurationMs: 1500, Text: "Hello", Style: "title_bold", Anchor: timeline.AnchorCenter, Animation: "pop_in"},
		}},
		&timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "word_highlight", Enabled: true},
	)
	got := mustCompile(t, withText, Final)

	if plain.Subtitles != nil {
		t.Error("a spec without text, captions or watermark has no subtitles")
	}
	if got.Subtitles == nil || got.Subtitles.TextEvents != 1 || !strings.Contains(got.Subtitles.ASS, "Hello") {
		t.Fatalf("subtitles %+v", got.Subtitles)
	}
	// Without a transcript an enabled captions track draws nothing.
	if got.Subtitles.CaptionEvents != 0 {
		t.Errorf("%d caption events without a transcript", got.Subtitles.CaptionEvents)
	}
	// The ass filter follows the join and nothing else changed: with it taken out, the graph is the plain one.
	const assStmt = ";[vj]ass=filename=subs.ass:fontsdir=fonts[vout]"
	if !strings.Contains(got.FilterComplex, assStmt) {
		t.Fatalf("no ass filter in %s", got.FilterComplex)
	}
	stripped := strings.NewReplacer(assStmt, "", "[vj]", "[vout]").Replace(got.FilterComplex)
	if stripped != plain.FilterComplex {
		t.Errorf("text changed the video or audio graph:\n%s\nvs\n%s", stripped, plain.FilterComplex)
	}
	for _, banned := range []string{"drawtext", "Hello"} {
		if strings.Contains(got.FilterComplex, banned) || strings.Contains(strings.Join(got.Args, " "), banned) {
			t.Errorf("the command contains %q: user text goes in the script, never in a filter argument", banned)
		}
	}
	// Text items are in the time map, without asset, source or speed.
	last := got.TimeMap[len(got.TimeMap)-1]
	if last.ItemID != "t1" || last.OutStartMs != 500 || last.OutEndMs != 2000 || last.AssetID != "" || last.SourceInMs != nil || last.Speed != nil {
		t.Errorf("text time map entry %+v", last)
	}
}

func TestTimeMap(t *testing.T) {
	items := []timeline.VideoItem{
		clipAt("c1", assetA, 3200, 5400, 0),
		clipAt("c2", assetB, 12000, 15000, 1900, withSpeed(1.25), withTransition(timeline.TransitionCrossfade, 300)),
		clipAt("c3", assetA, 1000, 4000, 4300, withSpeed(0.5)),
	}
	plan := mustCompile(t, specOf(30, items), Final)

	// c1 [0, 2200) is owned until c2 starts at 1900: the overlap belongs to the incoming item.
	want := []TimeMapEntry{
		{OutStartMs: 0, OutEndMs: 1900, ItemID: "c1", AssetID: assetA, SourceInMs: ptr(int64(3200)), Speed: ptr(1.0)},
		{OutStartMs: 1900, OutEndMs: 4300, ItemID: "c2", AssetID: assetB, SourceInMs: ptr(int64(12000)), Speed: ptr(1.25)},
		{OutStartMs: 4300, OutEndMs: 10300, ItemID: "c3", AssetID: assetA, SourceInMs: ptr(int64(1000)), Speed: ptr(0.5)},
	}
	if !reflect.DeepEqual(plan.TimeMap, want) {
		t.Errorf("time map\n got %+v\nwant %+v", plan.TimeMap, want)
	}
	if e, src, ok := plan.SourceTimeMs(1000); !ok || e.ItemID != "c1" || src != 3200+1000 {
		t.Errorf("t=1000 resolves to %+v %d %v", e, src, ok)
	}
	if e, src, ok := plan.SourceTimeMs(2900); !ok || e.ItemID != "c2" || src != 12000+1250 {
		t.Errorf("t=2900 resolves to %+v %d %v", e, src, ok)
	}
	if e, src, ok := plan.SourceTimeMs(4300 + 2000); !ok || e.ItemID != "c3" || src != 1000+1000 {
		t.Errorf("t=6300 resolves to %+v %d %v", e, src, ok)
	}
	if _, _, ok := plan.SourceTimeMs(10300); ok {
		t.Error("the end of the output belongs to no item")
	}
	data, err := plan.TimeMapJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"out_start_ms":0,"out_end_ms":1900,"item_id":"c1","asset_id":"`+assetA+`","source_in_ms":3200,"speed":1`) {
		t.Errorf("time map JSON %s", data)
	}
}

// D-03: a cue_only sound is never reproduced, in any profile. Its trend and anything of it stay out of the inputs and
// the arguments, the resolver is never asked about it, and Compile hands it to the export package as a cue.
func TestCueOnlySoundIsNeverRendered(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 6000, 0)},
		&timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
			&timeline.TrendSound{
				AudioCommon: timeline.AudioCommon{ID: "s1", AtMs: 500, SourceOffsetMs: 12000, Export: timeline.ExportCueOnly, GainDB: ptr(-3.0)},
				TrendID:     trendID,
			},
			&timeline.LicensedTrack{
				AudioCommon: timeline.AudioCommon{ID: "s2", AtMs: 0, SourceOffsetMs: 4000, Export: timeline.ExportLicensed, DurationMs: ptr(int64(5000))},
				AssetID:     assetMusic, LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f",
			},
		}})
	spec.Markers = []timeline.Marker{{AtMs: 0, Kind: timeline.MarkerDownbeat}, {AtMs: 500, Kind: timeline.MarkerBeat}}

	for _, prof := range Profiles() {
		var asked []string
		resolver := ResolverFunc(func(id string, v Variant) (ResolvedAsset, error) {
			asked = append(asked, id)
			return ResolvedAsset{Location: "/a/" + string(v) + "/" + id, HasAudio: true}, nil
		})
		plan, err := Compile(spec, prof, resolver)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range asked {
			if a == trendID || strings.HasPrefix(a, "trd_") {
				t.Errorf("%s: the resolver was asked for %s", prof.Name, a)
			}
		}
		all := strings.Join(plan.Args, "\x00") + plan.FilterComplex
		for _, in := range plan.Inputs {
			all += in.AssetID + in.Location + in.ItemID
		}
		if p := plan.Loudnorm; p != nil {
			all += strings.Join(p.Pass1Args, "\x00")
		}
		for _, banned := range []string{"trd_", "s1"} {
			if strings.Contains(all, banned) {
				t.Errorf("%s: the plan mentions the cue_only sound (%q)", prof.Name, banned)
			}
		}
		if len(plan.SoundCues) != 1 {
			t.Fatalf("%s: sound cues %+v", prof.Name, plan.SoundCues)
		}
		if got, want := plan.SoundCues[0], (SoundCue{ItemID: "s1", TrendID: trendID, AtMs: 500, SourceOffsetMs: 12000, DurationMs: 5500, GainDB: -3}); got != want {
			t.Errorf("%s: cue %+v, want %+v", prof.Name, got, want)
		}
		// The licensed track is rendered, so the rest of the mix still works.
		if len(plan.Inputs) != 2 || plan.Inputs[1].AssetID != assetMusic || plan.Inputs[1].Role != RoleAudio {
			t.Errorf("%s: inputs %+v", prof.Name, plan.Inputs)
		}
	}

	// With the licensed track gone, nothing but the cue is left: the final mix is silence, with no loudnorm to run.
	cueOnly := &timeline.TrendSound{AudioCommon: timeline.AudioCommon{ID: "s1", Export: timeline.ExportCueOnly}, TrendID: trendID}
	only := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 6000, 0)}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{cueOnly}})
	plan := mustCompile(t, only, Final)
	if plan.Loudnorm != nil || !strings.Contains(plan.FilterComplex, "[abed]anull[aout]") {
		t.Errorf("a timeline with only a cue_only sound must have a silent mix: %s", PrettyFilter(plan.FilterComplex))
	}
	if len(plan.Inputs) != 1 {
		t.Errorf("inputs %+v", plan.Inputs)
	}
}

// Compile refuses an audio item whose kind and export disagree: a trend sound that claims to be licensed must not be
// mixed in, and a licensed track that claims cue_only is not a cue.
func TestCueOnlyExportMustMatchKind(t *testing.T) {
	bad := []timeline.AudioItem{
		&timeline.TrendSound{AudioCommon: timeline.AudioCommon{ID: "s1", Export: timeline.ExportLicensed}, TrendID: trendID},
		&timeline.LicensedTrack{AudioCommon: timeline.AudioCommon{ID: "s1", Export: timeline.ExportCueOnly}, AssetID: assetMusic, LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f"},
	}
	for _, item := range bad {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0)}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{item}})
		if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%T with export %q: err %v, want ErrInvalidSpec", item, item.Common().Export, err)
		}
	}
}

func TestSourceAudio(t *testing.T) {
	items := []timeline.VideoItem{
		clipAt("c1", assetA, 1000, 3000, 0, withSourceAudio(-6)),
		clipAt("c2", assetB, 0, 4000, 1700, withSourceAudio(0), withSpeed(4), withTransition(timeline.TransitionCrossfade, 300)),
		clipAt("c3", assetA, 0, 2000, 2700), // no source audio
	}
	plan := mustCompile(t, specOf(30, items), Final)
	g := plan.FilterComplex
	for _, want := range []string{
		"[0:a]atrim=end=2.000,asetpts=PTS-STARTPTS,aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,volume=-6dB,afade=t=out:st=1.700000:d=0.300000,atrim=end=2.000,asetpts=PTS-STARTPTS[as0]",
		"[1:a]atrim=end=4.000,asetpts=PTS-STARTPTS,atempo=2,atempo=2,aresample=48000",
		"afade=t=in:st=0:d=0.300000",
		"adelay=1700:all=1[as1]",
		"amix=inputs=3:duration=first:normalize=0:dropout_transition=0,loudnorm=I=-14:TP=-1:LRA=11",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("graph lacks %q:\n%s", want, PrettyFilter(g))
		}
	}
	if strings.Contains(g, "[2:a]") {
		t.Error("a clip without use_source must not contribute audio")
	}
	// A clip that wants its source audio but has no audio stream plays silence.
	noAudio := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/a/" + id, HasAudio: false}, nil
	})
	silent, err := Compile(specOf(30, items[:1]), Final, noAudio)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(silent.FilterComplex, "[0:a]") || silent.Loudnorm != nil {
		t.Errorf("an asset without audio must not be mapped: %s", PrettyFilter(silent.FilterComplex))
	}
}

func TestAudioItems(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 10000, 0)},
		&timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
			&timeline.LicensedTrack{
				AudioCommon: timeline.AudioCommon{ID: "s1", AtMs: 1000, SourceOffsetMs: 4000, Export: timeline.ExportLicensed,
					DurationMs: ptr(int64(7000)), GainDB: ptr(-6.0), FadeInMs: ptr(int64(500)), FadeOutMs: ptr(int64(1500))},
				AssetID: assetMusic, LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f",
			},
			// No duration: plays to the end of the timeline, which is 10 s.
			&timeline.CreatorAudio{AudioCommon: timeline.AudioCommon{ID: "s2", AtMs: 2000, SourceOffsetMs: 0, Export: timeline.ExportLicensed}, AssetID: assetMusic},
		}})
	plan := mustCompile(t, spec, Final)
	for _, want := range []string{
		"[1:a]atrim=end=7.000,asetpts=PTS-STARTPTS,aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,afade=t=in:st=0:d=0.500,afade=t=out:st=5.500:d=1.500,adelay=1000:all=1,volume=-6dB[au1]",
		"[2:a]atrim=end=8.000,",
		"adelay=2000:all=1[au2]",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Errorf("graph lacks %q:\n%s", want, PrettyFilter(plan.FilterComplex))
		}
	}
	if got := plan.Inputs[1]; got.SeekMs != 4000 || got.DurationMs != 7000 {
		t.Errorf("input 1 %+v", got)
	}
	if got := plan.Inputs[2]; got.SeekMs != 0 || got.DurationMs != 8000 {
		t.Errorf("input 2 %+v", got)
	}
	ln := plan.Loudnorm
	if ln == nil || !ln.TwoPass || len(ln.Pass1Args) == 0 || !strings.Contains(strings.Join(ln.Pass1Args, " "), "print_format=json") {
		t.Fatalf("the final profile measures first: %+v", ln)
	}
	if !strings.Contains(plan.FilterComplex, ln.Filter) {
		t.Error("the single-pass filter must appear in the graph, where Execute swaps it")
	}
	if pre := mustCompile(t, spec, Preview); pre.Loudnorm == nil || pre.Loudnorm.TwoPass {
		t.Error("the preview normalises in a single dynamic pass")
	}
}

func TestBeatGuide(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 4000, 0)})
	spec.Markers = []timeline.Marker{
		{AtMs: 0, Kind: timeline.MarkerDownbeat},
		{AtMs: 500, Kind: timeline.MarkerBeat},
		{AtMs: 1000, Kind: timeline.MarkerBeat},
		{AtMs: 2000, Kind: timeline.MarkerDrop},
		{AtMs: 3000, Kind: timeline.MarkerSection},
		{AtMs: 9000, Kind: timeline.MarkerBeat}, // after the end
	}
	pre := mustCompile(t, spec, Preview)
	g := pre.FilterComplex
	for _, want := range []string{
		"sine=frequency=1000:sample_rate=48000:duration=4.000:samples_per_frame=48,volume='8*(between(t,0.5,0.53)+between(t,1,1.03))':eval=frame:precision=float,aformat=sample_fmts=fltp:channel_layouts=stereo[abeatb]",
		"sine=frequency=1500:sample_rate=48000:duration=4.000:samples_per_frame=48,volume='8*(between(t,0,0.03))':eval=frame:precision=float",
		"sine=frequency=2000:sample_rate=48000:duration=4.000:samples_per_frame=48,volume='8*(between(t,2,2.15))':eval=frame:precision=float",
		"[abeatb][abeatd][abeatp]amix=inputs=3:duration=first:normalize=0:dropout_transition=0,volume=-12dB[abeat]",
		// The beat guide goes on top of the core mix; the core without it is the audio stem (P3-12).
		"[abed]asplit=2[astem][acg]",
		"[acg][abeat]amix=inputs=2:duration=first:normalize=0:dropout_transition=0[aout]",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("graph lacks %q:\n%s", want, PrettyFilter(g))
		}
	}
	if fin := mustCompile(t, spec, Final); strings.Contains(fin.FilterComplex, "sine=") {
		t.Error("the final profile has no beat guide")
	}
	// Without markers the preview has no guide either.
	spec.Markers = nil
	if p := mustCompile(t, spec, Preview); strings.Contains(p.FilterComplex, "sine=") {
		t.Error("no markers, no guide")
	}
}

func TestLooksAndReframeInGraph(t *testing.T) {
	zoom, focus := 2.0, timeline.Point{X: 0.25, Y: 0.75}
	items := []timeline.VideoItem{
		func() timeline.VideoItem {
			it := clipAt("c1", assetA, 0, 2000, 0)
			it.Reframe = &timeline.Reframe{Mode: timeline.ReframeCover, Focus: &focus, Zoom: &zoom}
			it.Look = &timeline.Look{Preset: "bw"}
			return it
		}(),
		func() timeline.VideoItem {
			it := clipAt("c2", assetA, 0, 2000, 2000)
			it.Reframe = &timeline.Reframe{Mode: timeline.ReframeContain}
			return it
		}(),
	}
	spec := specOf(30, items)
	spec.Output.Background = "#102030"
	g := mustCompile(t, spec, Final).FilterComplex
	for _, want := range []string{
		"crop=1080:1920:x='clip(0.25*iw-540,0,iw-1080)':y='clip(0.75*ih-960,0,ih-1920)'",
		"crop=540:960,scale=1080:1920:flags=bicubic", // zoom 2: a centred crop of half the frame, scaled back
		"eq=saturation=0:contrast=1.1",
		"pad=1080:1920:(ow-iw)/2:(oh-ih)/2:color=0x102030",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("graph lacks %q:\n%s", want, PrettyFilter(g))
		}
	}
	bad := specOf(30, []timeline.VideoItem{func() timeline.VideoItem {
		it := clipAt("c1", assetA, 0, 2000, 0)
		it.Look = &timeline.Look{Preset: "sepia"}
		return it
	}()})
	if _, err := Compile(bad, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("unknown look: %v", err)
	}
}

func TestCompileRefusesWhatItCannotBuild(t *testing.T) {
	one := clipAt("c1", assetA, 0, 2000, 0)
	cases := map[string]*timeline.Spec{
		"no main track":    {Output: timeline.Output{Width: 1080, Height: 1920, FPS: 30}},
		"empty main track": specOf(30, nil),
		"first not at 0":   specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 500)}),
		"a gap":            specOf(30, []timeline.VideoItem{one, clipAt("c2", assetA, 0, 2000, 2500)}),
		"overlap, no fade": specOf(30, []timeline.VideoItem{one, clipAt("c2", assetA, 0, 2000, 1500)}),
		"fade, no overlap": specOf(30, []timeline.VideoItem{one, clipAt("c2", assetA, 0, 2000, 2000, withTransition(timeline.TransitionCrossfade, 300))}),
		"unknown fade":     specOf(30, []timeline.VideoItem{one, clipAt("c2", assetA, 0, 2000, 1700, withTransition("spin", 300))}),
		"plays nothing":    specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 2000, 2000, 0)}),
		"zero fps":         specOf(0, []timeline.VideoItem{one}),
		// The background is only read in contain mode.
		"bad background": func() *timeline.Spec {
			it := one
			it.Reframe = &timeline.Reframe{Mode: timeline.ReframeContain}
			s := specOf(30, []timeline.VideoItem{it})
			s.Output.Background = "red"
			return s
		}(),
		"unknown reframe":   specOf(30, []timeline.VideoItem{func() timeline.VideoItem { it := one; it.Reframe = &timeline.Reframe{Mode: "stretch"}; return it }()}),
		"zero-frame length": specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 10, 0)}),
	}
	for name, spec := range cases {
		if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err %v, want ErrInvalidSpec", name, err)
		}
	}
	if _, err := Compile(nil, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("nil spec: %v", err)
	}
	if _, err := Compile(specOf(30, []timeline.VideoItem{one}), Final, nil); err == nil {
		t.Error("nil resolver must be an error")
	}
	fail := ResolverFunc(func(string, Variant) (ResolvedAsset, error) { return ResolvedAsset{}, errors.New("no such asset") })
	if _, err := Compile(specOf(30, []timeline.VideoItem{one}), Final, fail); err == nil || !strings.Contains(err.Error(), "no such asset") {
		t.Errorf("a resolver error must come back: %v", err)
	}
}

func TestAtempoChain(t *testing.T) {
	cases := map[float64]string{
		1:    "",
		0.5:  "atempo=0.5",
		2:    "atempo=2",
		1.25: "atempo=1.25",
		0.25: "atempo=0.5,atempo=0.5",
		4:    "atempo=2,atempo=2",
		3:    "atempo=2,atempo=1.5",
		0.3:  "atempo=0.5,atempo=0.6",
	}
	for speed, want := range cases {
		if got := strings.Join(atempoChain(speed), ","); got != want {
			t.Errorf("speed %v: %q, want %q", speed, got, want)
		}
	}
}

func TestPrettyFilterAndFormatting(t *testing.T) {
	if got := PrettyFilter("a;b;c"); got != "a;\nb;\nc" {
		t.Error(got)
	}
	if secs(3200) != "3.200" || secs(5) != "0.005" || secs(0) != "0.000" {
		t.Error("secs")
	}
	if num(1.25) != "1.25" || num(-30) != "-30" || num(0.1+0.2) == "" {
		t.Error("num")
	}
	if scaledDim(1350, 2) != 676 || scaledDim(1080, 2) != 540 || scaledDim(1920, 1) != 1920 {
		t.Error("scaledDim")
	}
}

// A plan is plain data: it survives JSON, profile included, so it can be stored with a render and run by another process.
func TestPlanSurvivesJSON(t *testing.T) {
	cat := testAssets(t)
	for _, name := range []string{"v30_full_edit", "v26_audio_licensed_track", "v25_audio_trend_sound_cue_only"} {
		spec := loadFixture(t, name, cat)
		for _, prof := range Profiles() {
			plan, err := Compile(spec, prof, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var back Plan
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan, back) {
				t.Errorf("%s/%s: the plan changed in a JSON round trip (profile %+v and %+v)", name, prof.Name, plan.Profile, back.Profile)
			}
		}
	}
}

// The whip's blur runs while the transition does and not a frame longer: the window is end-exclusive.
func TestWhipBlurWindowIsEndExclusive(t *testing.T) {
	items := []timeline.VideoItem{
		clipAt("c1", assetA, 0, 2000, 0),
		clipAt("c2", assetB, 0, 2000, 1700, withTransition(timeline.TransitionWhip, 300)),
	}
	g := mustCompile(t, specOf(30, items), Final).FilterComplex
	if want := "gblur=sigma=12:enable='gte(t,1.700000)*lt(t,2.000000)'"; !strings.Contains(g, want) {
		t.Errorf("graph lacks %q:\n%s", want, PrettyFilter(g))
	}
}

func TestEveryLookWithCurvesIsInPlanarRGB(t *testing.T) {
	for name, f := range looks {
		if strings.Contains(f, "curves") && !strings.Contains(f, "format=gbrp,curves") {
			t.Errorf("look %s uses curves outside planar RGB: %s", name, f)
		}
	}
}

// A transition must overlap fewer frames than either clip it joins, and the two transitions of a clip together must leave
// a frame of it: Compile refuses what timeline.Validate refuses (transition_too_long), after snapping to frames.
func TestCompileRefusesTransitionsThatDoNotFit(t *testing.T) {
	cross := func(ms int64) func(*timeline.VideoItem) { return withTransition(timeline.TransitionCrossfade, ms) }
	cases := map[string][]timeline.VideoItem{
		// 399 ms into a 400 ms clip: both are 12 frames at 30 fps.
		"as long as the next clip": {clipAt("c1", assetA, 0, 2000, 0), clipAt("c2", assetA, 0, 400, 1601, cross(399))},
		// 2000 ms clip c1, then c2 of 800 ms with 500 ms in and 400 ms out: 15 + 12 frames of 24.
		"in and out together": {
			clipAt("c1", assetA, 0, 3000, 0),
			clipAt("c2", assetA, 0, 800, 2500, cross(500)),
			clipAt("c3", assetA, 0, 3000, 2900, withTransition(timeline.TransitionWhip, 400)),
		},
	}
	for name, items := range cases {
		if _, err := Compile(specOf(30, items), Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err %v, want ErrInvalidSpec", name, err)
		}
		// The validator agrees: the same spec raises transition_too_long.
		res := timeline.Validate(mustMarshal(t, specOf(30, items)), timeline.MemoryCatalog{assetA: {DurationMs: 60000, Kind: timeline.AssetVideo, Rights: timeline.RightsAttested}})
		var found bool
		for _, is := range res.Issues {
			if is.Code == timeline.CodeTransitionTooLong {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: Validate does not raise transition_too_long: %+v", name, res.Issues)
		}
	}
	// And a fitting one compiles and validates.
	ok := []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0), clipAt("c2", assetA, 0, 2000, 1700, cross(300))}
	if _, err := Compile(specOf(30, ok), Final, plainResolver()); err != nil {
		t.Error(err)
	}
}

func mustMarshal(t *testing.T, spec *timeline.Spec) []byte {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
