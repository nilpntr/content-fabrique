package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The editor's overviews lost 3 to 5 of their 8 frames on a long portrait proxy ("ffmpeg wrote no frame"). The cause was
// threads: ffmpeg gives every decoder and every mjpeg encoder one thread per CPU core, FramesArgs opens up to MaxFrameBatch of each
// in one process, and the sandbox caps a job's processes and threads, so on a many-core machine the encoder of some frames
// could not start (EAGAIN, "ff_frame_thread_encoder_init failed"). This runs the real command in the real sandbox with a
// PIDs cap that the unlimited-threads command exceeds on any machine with a few cores, on a proxy made the way ProxyArgs
// makes one (portrait, constant 30 fps, a keyframe every second) from a variable-frame-rate source, at the times of the live
// failure.
//
//	go test ./internal/media -run IntegrationFramesBatch -docker-image media-tools:dev
func TestIntegrationFramesBatchOnLongPortraitProxy(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := integrationRunner(t, root)
	ctx := context.Background()

	out := filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(inputs []Input, args ...string) {
		t.Helper()
		res, err := r.Run(ctx, Invocation{OutputDir: out, Inputs: inputs, Args: args, Timeout: 3 * time.Minute})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("ffmpeg: %+v %v", res, err)
		}
	}
	// A variable-rate "phone clip": 60 fps with random frames dropped, landscape.
	run(nil, "ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=60:duration=82",
		"-vf", "select='gt(random(0),0.4)'", "-fps_mode", "vfr", "-c:v", "libx264", "-preset", "ultrafast", "-g", "90", OutputPath("vfr.mp4"))
	// The proxy: upright portrait, constant 30 fps, one keyframe a second.
	run([]Input{{Host: filepath.Join(out, "vfr.mp4"), Container: InputPath(0)}}, "ffmpeg", "-nostdin", "-v", "error", "-y",
		"-i", InputPath(0), "-vf", "transpose=1,setsar=1,format=yuv420p", "-c:v", "libx264", "-preset", "ultrafast",
		"-fps_mode", "cfr", "-r", "30", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-movflags", "+faststart", OutputPath("proxy.mp4"))
	proxy := filepath.Join(out, "proxy.mp4")

	// Fewer processes than the unlimited-threads command needs on a machine with a handful of cores, more than the fixed one.
	r.Limits.PIDs = 96
	times := []int64{0, 2000, 7166, 21500, 24867, 49734, 62167, 74601}
	specs := make([]FrameSpec, len(times))
	for i, at := range times {
		specs[i] = FrameSpec{AtMs: at, Output: OutputPath(fmt.Sprintf("f%d.jpg", i))}
	}
	args, err := FramesArgs(InputPath(0), specs, 384)
	if err != nil {
		t.Fatal(err)
	}
	frames := filepath.Join(root, "frames")
	if err := os.Mkdir(frames, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(ctx, Invocation{OutputDir: frames, Inputs: []Input{{Host: proxy, Container: InputPath(0)}}, Args: args})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("frames: %+v %v", res, err)
	}
	var first []byte
	for i := range times {
		data, err := os.ReadFile(filepath.Join(frames, fmt.Sprintf("f%d.jpg", i)))
		if err != nil || len(data) < 100 || !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
			t.Fatalf("frame %d (at %d ms): %d bytes, %v", i, times[i], len(data), err)
		}
		if i == 0 {
			first = data
		} else if bytes.Equal(first, data) {
			t.Errorf("frame %d (at %d ms) is the same picture as frame 0: the seek did nothing", i, times[i])
		}
	}
}
