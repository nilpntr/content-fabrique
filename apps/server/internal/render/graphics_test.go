package render

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func graphicsResolver(dir string) AssetResolver {
	return ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: filepath.Join(dir, id+".mp4"), HasAudio: true}, nil
	})
}

// The graphics fixture compiles to a motion job and an overlay chain: one image-sequence input per segment after the clips and the
// audio, each shifted to its segment's start and laid over the joined clips before the ASS script (so the watermark stays on top),
// scaled down for the preview.
func TestGraphicsCompileToOverlays(t *testing.T) {
	spec := loadFixture(t, "v50_graphics_9x16", testAssets(t))
	for _, prof := range Profiles() {
		plan, err := Compile(spec, prof, graphicsResolver("/assets"))
		if err != nil {
			t.Fatal(err)
		}
		m := plan.Motion
		if m == nil || len(m.Job.Items) != 8 || m.BundleHash != motion.BundleHash() {
			t.Fatalf("%s: motion plan %+v", prof.Name, m)
		}
		if m.Job.Width != 1080 || m.Job.Height != 1920 {
			t.Errorf("%s: the job renders at %dx%d, not the output's own size", prof.Name, m.Job.Width, m.Job.Height)
		}
		args := strings.Join(plan.Args, " ")
		if !strings.Contains(args, "-f image2 -framerate 30 -start_number 0 -i motion/s0/f%06d.png") {
			t.Errorf("%s: no overlay input: %s", prof.Name, args)
		}
		g := plan.FilterComplex
		base := len(plan.Inputs)
		ov := strings.Index(g, "overlay=")
		ass := strings.Index(g, "ass=")
		if ov < 0 || (ass >= 0 && ov > ass) {
			t.Errorf("%s: the overlays must come before the ASS script: %s", prof.Name, PrettyFilter(g))
		}
		if !strings.Contains(g, "["+itoa(base)+":v]setpts=PTS-STARTPTS+0.000000/TB") {
			t.Errorf("%s: the first overlay input is not input %d", prof.Name, base)
		}
		if scaled := strings.Contains(g, ":flags=area"); scaled != (prof.SizeDivisor > 1) {
			t.Errorf("%s: scale of the overlays (%v) does not follow the size divisor %d", prof.Name, scaled, prof.SizeDivisor)
		}
		ids := map[string]bool{}
		for _, e := range plan.TimeMap {
			ids[e.ItemID] = true
		}
		for _, it := range m.Job.Items {
			if !ids[it.ID] {
				t.Errorf("%s: %s is not in the time map", prof.Name, it.ID)
			}
		}
	}
}

// With motion graphics off, every graphics item is drawn as ASS text in its box with its template's fallback animation, and the plan
// has no motion job and no overlay.
func TestGraphicsFallBackToASSText(t *testing.T) {
	spec := loadFixture(t, "v50_graphics_9x16", testAssets(t))
	prof := Preview
	prof.MotionFallback = true
	plan, err := Compile(spec, prof, graphicsResolver("/assets"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Motion != nil || strings.Contains(plan.FilterComplex, "overlay=") {
		t.Fatal("the fallback compiled overlays")
	}
	if plan.Subtitles == nil || plan.Subtitles.TextEvents != 8 {
		t.Fatalf("subtitles %+v, want 8 text events", plan.Subtitles)
	}
	for _, want := range []string{"BACK DAY 101", "1. SHRUGS VOOR UPPER TRAPS", "12 REPS", "LAT PULLDOWN"} {
		if !strings.Contains(plan.Subtitles.ASS, want) && !strings.Contains(strings.ReplaceAll(plan.Subtitles.ASS, `\N`, " "), want) {
			t.Errorf("the fallback script lacks %q", want)
		}
	}
	// The same box: the fallback text sits where the overlay would.
	motionPlan, err := Compile(spec, Preview, graphicsResolver("/assets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range motionPlan.Motion.Job.Items {
		g := findGraphic(t, spec, it.ID)
		pl, err := timeline.PlaceText(spec.Output, g.AsText())
		if err != nil {
			t.Fatal(err)
		}
		if float64(pl.Box.Left) != it.Box.X || float64(pl.Box.Top) != it.Box.Y {
			t.Errorf("%s: the overlay box %+v is not the text box %v", it.ID, it.Box, pl.Box)
		}
	}
}

func findGraphic(t *testing.T, spec *timeline.Spec, id string) timeline.GraphicsItem {
	t.Helper()
	for _, tr := range spec.Tracks {
		if g, ok := tr.(*timeline.GraphicsTrack); ok {
			for _, it := range g.Items {
				if it.ID == id {
					return it
				}
			}
		}
	}
	t.Fatalf("no graphics item %s", id)
	return timeline.GraphicsItem{}
}

// Never over the body (P6-06's rule for text holds for overlays): with the person filling the frame for the whole video there is no
// time an overlay that avoids them fits, so each is left out with a warning; one set not to avoid the subject is still drawn.
func TestGraphicsNeverOverTheBody(t *testing.T) {
	spec := loadFixture(t, "v50_graphics_9x16", testAssets(t))
	full := &subject.Track{Aspect: 0.5625, Samples: []subject.Sample{
		{AtMs: 0, X: 0.02, Y: 0.04, W: 0.96, H: 0.96, HeadTopY: 0.05},
		{AtMs: 60000, X: 0.02, Y: 0.04, W: 0.96, H: 0.96, HeadTopY: 0.05},
	}}
	res := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/assets/" + id + ".mp4", HasAudio: true, Subject: full}, nil
	})
	plan, err := Compile(spec, Preview, res)
	if err != nil {
		t.Fatal(err)
	}
	var drawn []string
	if plan.Motion != nil {
		for _, it := range plan.Motion.Job.Items {
			drawn = append(drawn, it.ID)
		}
	}
	// g7 is avoid_subject false in the fixture: it stays where it was put.
	if len(drawn) != 1 || drawn[0] != "g7" {
		t.Fatalf("drawn %v, want only g7", drawn)
	}
	dropped := 0
	for _, w := range plan.Warnings {
		if w.Code == "text_over_subject" && strings.Contains(w.Message, "is left out") {
			dropped++
		}
	}
	if dropped != 7 {
		t.Fatalf("%d items left out with a warning, want 7: %+v", dropped, plan.Warnings)
	}
}

func TestGraphicsRefuseAStalePlan(t *testing.T) {
	spec := loadFixture(t, "v50_graphics_9x16", testAssets(t))
	plan, err := Compile(spec, Preview, graphicsResolver("/assets"))
	if err != nil {
		t.Fatal(err)
	}
	plan.Motion.BundleHash = "0000"
	err = runMotion(context.Background(), plan, nil, t.TempDir(), ExecuteOptions{}, true)
	if err == nil || !strings.Contains(err.Error(), "another template bundle") {
		t.Fatalf("a plan compiled against another bundle ran: %v", err)
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + intString(n)) }

func intString(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// The graphics fixture rendered through the docker provider and the media-tools image, both profiles: cf-motion renders the overlays
// in the sandbox, ffmpeg composites them, and the output has the plan's size and length. CI's media-tools job runs it.
func TestIntegrationGraphicsRenderInDocker(t *testing.T) {
	if *dockerImage == "" {
		t.Skip("set -docker-image to run the Docker integration test")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := sandbox.NewDocker(sandbox.Policy{AllowedRoots: []string{root}})
	d.Runtime = *dockerRuntime
	runner := &media.SandboxRunner{Provider: d, Image: *dockerImage, Limits: DefaultLimits()}
	ctx := context.Background()
	spec := loadFixture(t, "v50_graphics_9x16", testAssets(t))
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := runner.Run(ctx, media.Invocation{OutputDir: src, Args: []string{"ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=17", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000:duration=17",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", media.OutputPath("ast_01j3er3kwcgrasdgd4aznmxxe0.mp4")}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate the source: %+v %v", res, err)
	}
	for _, prof := range Profiles() {
		t.Run(string(prof.Name), func(t *testing.T) {
			plan, err := Compile(spec, prof, graphicsResolver(src))
			if err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(root, string(prof.Name)+"-work")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, string(prof.Name)+".mp4")
			var rep motion.Report
			if err := Execute(ctx, plan, runner, ExecuteOptions{Output: out, WorkDir: work, Motion: MotionOptions{Report: func(r motion.Report) { rep = r }}}); err != nil {
				t.Fatal(err)
			}
			if rep.Frames != plan.Motion.Job.TotalFrames() {
				t.Errorf("the motion run rendered %d frames, the job has %d", rep.Frames, plan.Motion.Job.TotalFrames())
			}
			t.Logf("%s: %d overlay frames in %v (chrome %s, frames %d ms)", prof.Name, rep.Frames, rep.Duration, rep.Chrome, rep.Timing.FramesMs)
			m, err := media.Probe(ctx, runner, out)
			if err != nil {
				t.Fatal(err)
			}
			if m.Width != plan.OutputSize.Width || m.Height != plan.OutputSize.Height || math.Abs(float64(m.DurationMs-plan.DurationMs)) > 100 {
				t.Errorf("output %dx%d, %d ms; plan %+v, %d ms", m.Width, m.Height, m.DurationMs, plan.OutputSize, plan.DurationMs)
			}
		})
	}
}

// A song and motion graphics in one edit: the song is an input after the clips, and the overlay segments come after it. Before the
// audio graph was built ahead of the video graph, the first overlay segment was numbered as the song's input, so ffmpeg read [k:v]
// from an audio-only file and every render of an edit with a song and a title failed ("Stream specifier ':v' ... matches no streams").
func TestGraphicsWithASongNumberTheOverlaysAfterIt(t *testing.T) {
	spec := loadFixture(t, "v50_graphics_9x16", testAssets(t))
	spec.Tracks = append(spec.Tracks, &timeline.AudioTrack{ID: "music", Items: []timeline.AudioItem{
		&timeline.CreatorAudio{AudioCommon: timeline.AudioCommon{ID: "song", Export: timeline.ExportLicensed}, AssetID: "ast_song"},
	}})
	for _, prof := range Profiles() {
		plan, err := Compile(spec, prof, graphicsResolver("/assets"))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Motion == nil {
			t.Fatalf("%s: no motion plan", prof.Name)
		}
		song := -1
		for _, in := range plan.Inputs {
			if in.ItemID == "song" {
				song = in.Index
			}
		}
		if song < 0 {
			t.Fatalf("%s: the song is not an input: %+v", prof.Name, plan.Inputs)
		}
		base := len(plan.Inputs)
		if song >= base {
			t.Fatalf("%s: the song is input %d, not before the overlays at %d", prof.Name, song, base)
		}
		g := plan.FilterComplex
		if strings.Contains(g, "["+itoa(song)+":v]") {
			t.Errorf("%s: the graph reads video from the song's input %d: %s", prof.Name, song, PrettyFilter(g))
		}
		if !strings.Contains(g, "["+itoa(base)+":v]setpts=PTS-STARTPTS+") {
			t.Errorf("%s: the first overlay is not input %d, right after the clips and the song", prof.Name, base)
		}
		// The -i arguments are in index order: the overlays' patterns come after every regular input.
		args := strings.Join(plan.Args, " ")
		if i, j := strings.Index(args, "-i /assets/ast_song"), strings.Index(args, "-i motion/s0/"); i < 0 || j < 0 || i > j {
			t.Errorf("%s: input order: song at %d, first overlay at %d", prof.Name, i, j)
		}
	}
}
