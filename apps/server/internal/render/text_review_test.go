package render

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// inTransition reports whether a frame is inside a transition of the plan, where the words of both clips are spoken.
func inTransition(plan Plan, f int64) bool {
	for _, c := range plan.CutList {
		if c.TransitionFrames > 0 && f >= c.Frame && f <= c.Frame+c.TransitionFrames {
			return true
		}
	}
	return false
}

func oneClipSpec(tracks ...timeline.Track) *timeline.Spec {
	return &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: append([]timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_01j3er3kwcgrasdgd4aznmxxe0", Source: timeline.Source{InMs: 0, OutMs: 5000}, AtMs: 0}}}}, tracks...),
	}
}

// The typewriter finishes starting its last character within 60 % of the event, for every allowed length and duration, also the
// extremes: 120 characters on a 300 ms event.
func TestTypewriterRevealsWithinSixtyPercent(t *testing.T) {
	tRe := regexp.MustCompile(`\\t\((\d+),(\d+),`)
	for _, c := range []struct {
		text string
		dur  int64
	}{
		{strings.Repeat("abcdefghij", 12), 300},
		{strings.Repeat("abcdefghij", 12), 1000},
		{strings.Repeat("ab ", 40), 300},
		{"Hi", 300},
		{"Typed out", 1500},
		{"Typed out", 60000},
	} {
		it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: c.dur, Text: c.text, Style: "subtitle_clean", Anchor: "upper_third", Animation: "typewriter"}
		plan, err := Compile(oneClipSpec(&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{it}}), Final, plainResolver())
		if err != nil {
			t.Fatal(err)
		}
		var d dialogue
		for _, x := range parseDialogues(t, plan.Subtitles.ASS) {
			d = x
		}
		times := tRe.FindAllStringSubmatch(d.text, -1)
		chars := len(strings.ReplaceAll(c.text, " ", ""))
		if len(times) != chars {
			t.Fatalf("%.10q/%d ms: %d reveals for %d characters", c.text, c.dur, len(times), chars)
		}
		last := 0
		for _, m := range times {
			start, _ := strconv.Atoi(m[1])
			if start < last {
				t.Fatalf("reveal times go back: %d after %d", start, last)
			}
			last = start
		}
		if float64(last) > 0.6*float64(c.dur) {
			t.Errorf("%.10q/%d ms: the last character starts at %d ms, after 60 %% of the event (%.0f ms)", c.text, c.dur, last, 0.6*float64(c.dur))
		}
	}
}

// An omitted animation is none, everywhere: the style registry has no default animation.
func TestOmittedAnimationIsNone(t *testing.T) {
	it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "Hello", Style: "stat_big", Anchor: "center"}
	if it.AnimationOrDefault() != "none" {
		t.Fatalf("AnimationOrDefault = %q", it.AnimationOrDefault())
	}
	plan, err := Compile(oneClipSpec(&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{it}}), Final, plainResolver())
	if err != nil {
		t.Fatal(err)
	}
	body := parseDialogues(t, plan.Subtitles.ASS)[0].text
	for _, tag := range []string{`\fad`, `\move`, `\fscx`, `\t(`} {
		if strings.Contains(body, tag) {
			t.Errorf("an item with no animation has %s in %q", tag, body)
		}
	}
}

// Sizes are ems: the em in pixels is the same for every font at the same EmPct, and a caption is about 5 % of 1920 px.
func TestStyleSizesAreEms(t *testing.T) {
	for _, st := range style.All() {
		size := st.FontSizePx(1080)
		tab := style.FontTableOf(st)
		em := float64(size) * float64(tab.UnitsPerEm) / float64(tab.WinAscent+tab.WinDescent)
		if want := st.EmPx(1080); em < want-1 || em > want+1 {
			t.Errorf("%s: em %.1f px, want %.1f", st.ID, em, want)
		}
	}
	caption, _ := style.Lookup("caption_plain")
	if em := caption.EmPx(1080); em < 94 || em > 98 {
		t.Errorf("a caption is 5 %% of 1920 px = 96 px, got %.1f", em)
	}
	if caption.EmPx(540) != caption.EmPx(1080)/2 {
		t.Error("sizes scale with the shorter side")
	}
}

// Captions are confined to the band. A transcript with a word that cannot fit gets that word shrunk or, if even that is not
// enough, left out with a warning; nothing is ever drawn outside the zone.
func TestCaptionsStayInTheBand(t *testing.T) {
	huge := strings.Repeat("W", 60)
	words := []Word{
		{Text: "hello", StartMs: 0, EndMs: 300},
		{Text: "world", StartMs: 400, EndMs: 700},
		{Text: huge, StartMs: 800, EndMs: 1100},
		{Text: "again", StartMs: 1200, EndMs: 1500},
		{Text: strings.Repeat("W", 12), StartMs: 1600, EndMs: 1900},
	}
	res := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/a/" + id, HasAudio: true, Transcript: words}, nil
	})
	for _, kind := range []string{style.CaptionChunk, style.CaptionWordHighlight} {
		spec := oneClipSpec(&timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: kind, Enabled: true})
		plan, err := Compile(spec, Final, res)
		if err != nil {
			t.Fatal(err)
		}
		zone, _ := spec.Output.Zone()
		st, _ := style.CaptionStyle(kind)
		band := style.CaptionBand(zone, 1080, 1080, st)
		if !zone.Safe(band) {
			t.Fatalf("the band %+v is not safe", band)
		}
		if len(plan.Warnings) == 0 || plan.Warnings[0].Code != "caption_dropped" || !strings.Contains(plan.Warnings[0].Message, "WWWWWWWW") {
			t.Errorf("%s: warnings %+v, want the huge word dropped", kind, plan.Warnings)
		}
		posRe := regexp.MustCompile(`\\pos\(([\d.]+),([\d.]+)\)`)
		for _, d := range parseDialogues(t, plan.Subtitles.ASS) {
			if d.layer != layerCaption {
				continue
			}
			m := posRe.FindStringSubmatch(d.text)
			cy, _ := strconv.ParseFloat(m[2], 64)
			if cy < float64(band.Top) || cy > float64(band.Bottom) {
				t.Errorf("%s: a caption is centred at y %v, outside the band %+v", kind, cy, band)
			}
		}
		// The 12 W word is wide: it was shrunk (a scaled style line exists) or wrapped, never left to overflow.
		if !strings.Contains(plan.Subtitles.ASS, "WWWWWWWWWWWW") && !strings.Contains(plan.Subtitles.ASS, "WWWW") {
			t.Errorf("%s: the long word is missing", kind)
		}
	}
}

// Characters a font cannot draw are left out of captions and text, so everything that is drawn was measured.
func TestUnsupportedCharactersAreDropped(t *testing.T) {
	res := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/a/" + id, HasAudio: true, Transcript: []Word{{Text: "go\U0001F600", StartMs: 0, EndMs: 300}, {Text: "\U0001F600", StartMs: 400, EndMs: 700}, {Text: "日本", StartMs: 800, EndMs: 1100}}}, nil
	})
	spec := oneClipSpec(&timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "chunk", Enabled: true},
		&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "ok \U0001F600!", Style: "title_bold", Anchor: "upper_third"}}})
	plan, err := Compile(spec, Final, res)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"\U0001F600", "日", "本"} {
		if strings.Contains(plan.Subtitles.ASS, r) {
			t.Errorf("%q reached the script", r)
		}
	}
	if !strings.Contains(plan.Subtitles.ASS, "GO") || !strings.Contains(plan.Subtitles.ASS, "ok !") {
		t.Errorf("the rest of the text is missing:\n%s", plan.Subtitles.ASS)
	}
}

// During a crossfade the outgoing clip's words last until its own audio ends, past the start of the incoming clip.
func TestCaptionsDuringACrossfade(t *testing.T) {
	spec, plan := fixtureSpecAndPlan(t, "v34_captions_speed_cut_9x16", Final)
	cut := plan.CutList[0]
	if cut.Transition != timeline.TransitionCrossfade {
		t.Fatalf("cut %+v", cut)
	}
	main, _, _ := timeline.MainTrack(spec)
	c1End := timeline.Frame(timeline.EndMs(main.Items[0]), plan.FPS)
	var maxEnd int64
	for _, d := range parseDialogues(t, plan.Subtitles.ASS) {
		if d.layer != layerCaption {
			continue
		}
		if s := firstFrameAt(t, d.start, plan.FPS); s < cut.Frame {
			maxEnd = max(maxEnd, firstFrameAt(t, d.end, plan.FPS))
		}
	}
	if maxEnd <= cut.Frame {
		t.Errorf("the outgoing clip's captions end on frame %d, at or before the incoming clip starts (%d): they should last to its end, frame %d", maxEnd, cut.Frame, c1End)
	}
}

// A render inside the media-tools image uses the image's own fonts: nothing is staged, the filter reads /usr/share/fonts/cf, and
// it works outside a repository checkout. The local provider stages the host's copy next to the script.
func TestFontsComeFromTheImageInDockerAndAreStagedLocally(t *testing.T) {
	cat := testAssets(t)
	spec := loadFixture(t, "v32_text_all_styles_9x16", cat)
	plan, err := Compile(spec, Preview, fakeResolver(cat))
	if err != nil {
		t.Fatal(err)
	}
	fontsAbs, err := filepath.Abs(filepath.Join(fixturesDir, "../../../media-tools/fonts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // not in a checkout: the font search finds nothing

	for _, tc := range []struct {
		provider string
		inImage  bool
	}{{"docker", true}, {"hardened", true}, {"local", false}} {
		t.Run(tc.provider, func(t *testing.T) {
			work := t.TempDir()
			prov := &fakeProvider{name: tc.provider}
			runner := &media.SandboxRunner{Provider: prov, Image: "media-tools:test", Limits: DefaultLimits()}
			err := Execute(context.Background(), plan, runner, ExecuteOptions{Output: filepath.Join(t.TempDir(), "o.mp4"), WorkDir: work})
			if !tc.inImage {
				if err == nil || !strings.Contains(err.Error(), "font files not found") {
					t.Fatalf("the local provider outside a checkout and without FontsDir must say it has no fonts, got %v", err)
				}
				// With FontsDir they are staged.
				if err := Execute(context.Background(), plan, runner, ExecuteOptions{Output: filepath.Join(t.TempDir(), "o.mp4"), WorkDir: work, FontsDir: fontsAbs}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(work, "fonts", "Anton-Regular.ttf")); err != nil {
					t.Errorf("fonts not staged: %v", err)
				}
				if !strings.Contains(prov.joined(), "fontsdir=fonts") {
					t.Errorf("local graph: %s", prov.joined())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(work, "fonts")); err == nil {
				t.Error("fonts were staged for a render in the image")
			}
			if _, err := os.Stat(filepath.Join(work, "subs.ass")); err != nil {
				t.Errorf("script not staged: %v", err)
			}
			if j := prov.joined(); !strings.Contains(j, "fontsdir="+ImageFontsDir) || strings.Contains(j, "fontsdir=fonts") {
				t.Errorf("graph does not read the image's fonts: %s", j)
			}
		})
	}
}

// fakeProvider is a sandbox.Provider with a name that records what it was asked to run and writes the output.
type fakeProvider struct {
	name string
	jobs []sandbox.Job
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Run(_ context.Context, job sandbox.Job) (sandbox.Result, error) {
	f.jobs = append(f.jobs, job)
	for _, m := range job.Mounts {
		if m.Writable {
			_ = os.WriteFile(filepath.Join(m.Host, "out.mp4"), []byte("mp4"), 0o600)
		}
	}
	return sandbox.Result{}, nil
}

func (f *fakeProvider) joined() string {
	var all []string
	for _, j := range f.jobs {
		all = append(all, strings.Join(j.Args, " "))
	}
	return strings.Join(all, "\n")
}
