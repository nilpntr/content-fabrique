package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Draws text with the ffmpeg and libass of the media-tools image, in the sandbox as production runs it (read-only root, no
// network, fonts and script staged in the work directory), and checks that the ink lies inside the layout box that
// timeline.Validate checked. This is the layout contract on the real renderer: it needs Docker and an image, like the render
// integration test.
//
//	go test ./internal/render -run IntegrationText -docker-image media-tools:dev
func TestIntegrationTextInDocker(t *testing.T) {
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

	cases := []struct {
		format timeline.Format
		item   timeline.TextItem
	}{
		{timeline.Format9x16, timeline.TextItem{Text: "Never skip leg day", Style: "title_bold", Anchor: "upper_third"}},
		{timeline.Format9x16, timeline.TextItem{Text: "SQUAT DEEP", Style: "title_outline", Anchor: "center"}},
		{timeline.Format9x16, timeline.TextItem{Text: "Barbell back squat", Style: "label_box", Anchor: "lower_third"}},
		{timeline.Format9x16, timeline.TextItem{Text: "5x5", Style: "stat_big", Position: &timeline.Point{X: 0.4, Y: 0.4}}},
		{timeline.Format16x9, timeline.TextItem{Text: "Rest 90 seconds\nbetween sets", Style: "subtitle_clean", Anchor: "lower_third"}},
		{timeline.Format4x5, timeline.TextItem{Text: "Drive through your heels", Style: "caption_highlight", Anchor: "center"}},
		{timeline.Format1x1, timeline.TextItem{Text: `{\an9\fs200}literal`, Style: "caption_plain", Anchor: "center"}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d_%s_%s", i, tc.format, tc.item.Style), func(t *testing.T) {
			tc.item.ID, tc.item.DurationMs, tc.item.Animation = "t1", 3000, "none"
			plan, pl := textPlan(t, tc.format, tc.item)
			work := filepath.Join(root, fmt.Sprintf("work%d", i))
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := stageSubtitles(plan, work, "", true); err != nil {
				t.Fatal(err)
			}
			w, h := plan.OutputSize.Width, plan.OutputSize.Height
			res, err := runner.Run(context.Background(), media.Invocation{OutputDir: work, Args: []string{"ffmpeg", "-nostdin", "-v", "error", "-y",
				"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02X%02X%02X:s=%dx%d:r=30:d=5", inkBackground, inkBackground, inkBackground, w, h),
				"-vf", useImageFonts([]string{plan.Subtitles.filter()}, plan.Subtitles)[0], "-ss", "1.0", "-frames:v", "1", media.OutputPath("out.png")}})
			if err != nil || res.ExitCode != 0 {
				t.Fatalf("ffmpeg in the image: %+v %v", res, err)
			}
			ink, ok := inkBox(readPNG(t, filepath.Join(work, "out.png")))
			if !ok {
				t.Fatal("nothing drawn")
			}
			zone := pl.Zone
			if !within(ink, pl.Box, 0) {
				t.Errorf("ink %+v is outside the layout box %+v", ink, pl.Box)
			}
			if !zone.Safe(ink) {
				t.Errorf("ink %+v is outside the safe zone %+v", ink, zone)
			}
			if ink.Width()*10 < pl.Box.Width()*5 && len(pl.Layout.Lines) == 1 {
				t.Errorf("ink %+v is much narrower than the box %+v", ink, pl.Box)
			}
		})
	}
}
