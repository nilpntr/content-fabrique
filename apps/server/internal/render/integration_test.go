package render

import (
	"context"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Needs Docker and a built media-tools image, so it only runs when asked (CI's media-tools job does):
//
//	go test ./internal/render -run Integration -docker-image media-tools:dev [-docker-runtime runsc]
var (
	dockerImage   = flag.String("docker-image", "", "media-tools image for the Docker integration test")
	dockerRuntime = flag.String("docker-runtime", "", "container runtime, for example runsc")
)

// Renders the full-edit fixture through the docker provider and the media-tools image, as production does: inputs mounted
// read-only at /in/<n>, no network, a read-only root, output and progress in the work directory. It needs nothing on the
// host: the sources are generated and the result is probed by the image. The image's ffmpeg is not the one the golden
// frames were made with, so this checks what the render is made of (size, length, audio) and not pixels.
func TestIntegrationRenderInDocker(t *testing.T) {
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

	// Sources: 16 s of video with audio for every asset the fixture uses (it reads up to 15 s).
	cat := testAssets(t)
	spec := loadFixture(t, "v30_full_edit", cat)
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	main, _, _ := timeline.MainTrack(spec)
	for _, it := range main.Items {
		file := filepath.Join(src, it.AssetID+".mp4")
		if _, err := os.Stat(file); err == nil {
			continue
		}
		res, err := runner.Run(ctx, media.Invocation{OutputDir: src, Args: []string{"ffmpeg", "-nostdin", "-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=16", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000:duration=16",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", media.OutputPath(it.AssetID + ".mp4")}})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("generate %s: %+v %v", it.AssetID, res, err)
		}
	}
	resolver := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: filepath.Join(src, id+".mp4"), HasAudio: true}, nil
	})

	for _, prof := range Profiles() {
		t.Run(string(prof.Name), func(t *testing.T) {
			plan, err := Compile(spec, prof, resolver)
			if err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(root, string(prof.Name)+"-work")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, string(prof.Name)+".mp4")
			var last Progress
			err = Execute(ctx, plan, runner, ExecuteOptions{Output: out, WorkDir: work, Progress: func(p Progress) { last = p }})
			if err != nil {
				t.Fatal(err)
			}
			m, err := media.Probe(ctx, runner, out)
			if err != nil {
				t.Fatal(err)
			}
			if m.Width != plan.OutputSize.Width || m.Height != plan.OutputSize.Height || !m.HasAudio || !m.HasVideo ||
				math.Abs(float64(m.DurationMs-plan.DurationMs)) > 100 {
				t.Errorf("output %dx%d, %d ms, audio %v; plan %+v, %d ms", m.Width, m.Height, m.DurationMs, m.HasAudio, plan.OutputSize, plan.DurationMs)
			}
			if !last.Done || last.Fraction != 1 {
				t.Errorf("last progress %+v: the progress file of the sandboxed ffmpeg was not read", last)
			}
		})
	}
}
