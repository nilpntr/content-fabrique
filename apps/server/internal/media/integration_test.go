package media

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// Needs Docker and a built media-tools image, so it only runs when asked (CI does):
//
//	go test ./internal/media -run Integration -docker-image media-tools:dev [-docker-runtime runsc]
var (
	dockerImage   = flag.String("docker-image", "", "media-tools image for the Docker integration tests")
	dockerRuntime = flag.String("docker-runtime", "", "container runtime, for example runsc")
)

func integrationRunner(t *testing.T, root string) *SandboxRunner {
	t.Helper()
	if *dockerImage == "" {
		t.Skip("set -docker-image to run the Docker integration tests")
	}
	d := sandbox.NewDocker(sandbox.Policy{AllowedRoots: []string{root}})
	d.Runtime = *dockerRuntime
	return &SandboxRunner{Provider: d, Image: *dockerImage, Limits: sandbox.Limits{CPU: 2, MemoryMB: 1024, Timeout: 2 * time.Minute, PIDs: 256}}
}

func TestIntegrationProbeAndFormatWhitelist(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := integrationRunner(t, root)
	ctx := context.Background()

	// A real clip, made by the image itself: HDR (HLG) 640x360 with audio.
	out := filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(ctx, Invocation{OutputDir: out, Args: []string{"ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=2", "-f", "lavfi", "-i", "sine=duration=2",
		"-vf", "setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-metadata:s:v", "rotate=0", OutputPath("hlg.mp4")}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate: %+v %v", res, err)
	}
	m, err := Probe(ctx, r, filepath.Join(out, "hlg.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HDR || m.HDRFormat != "hlg" || m.Width != 640 || m.Height != 360 || m.Container != "mp4" || m.SelectedAudio == nil || m.DurationMs < 1900 {
		t.Fatalf("%+v", m)
	}

	// A playlist that points at another file is not an accepted format: probing it must fail without reading the target.
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"concat.txt": "ffconcat version 1.0\nfile '" + secret + "'\n",
		"list.m3u8":  "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n" + secret + "\n#EXT-X-ENDLIST\n",
	} {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Probe(ctx, r, p)
		if err == nil {
			t.Fatalf("%s: a playlist must not be probed", name)
		}
		var te *ToolError
		if !errors.As(err, &te) || (name == "concat.txt" && !strings.Contains(te.Stderr, "whitelist")) {
			t.Fatalf("%s: want a ToolError (for the concat list: about the format whitelist), got %v", name, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: the target's content leaked: %v", name, err)
		}
	}
}
