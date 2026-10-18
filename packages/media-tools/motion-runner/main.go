// Command cf-motion renders the motion-graphics overlay frames of one render inside the media sandbox
// (docs/design/motion-graphics-v1.md). It starts chrome-headless-shell with the DevTools protocol on a pipe, opens the
// template bundle's scene from a read-only mount, mounts the job's overlay items, and for every frame of the job's segments
// seeks the page's virtual clock to the frame's time and captures a transparent PNG. Anything wrong in the page (an
// exception, a refused request, a font that did not load, a blank or spilling frame) fails the run with a message.
//
//	cf-motion --chrome /opt/chrome/chrome-headless-shell --bundle /in/0 --fonts /usr/share/fonts/cf \
//	          --job /work/motion-job.json --out /work/motion
//
// It uses the standard library only and is built statically into the media-tools image.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var o Options
	fs := flag.NewFlagSet("cf-motion", flag.ContinueOnError)
	fs.StringVar(&o.Chrome, "chrome", "/opt/chrome/chrome-headless-shell", "the chrome-headless-shell binary")
	fs.StringVar(&o.Bundle, "bundle", "", "the template bundle directory (holds scene.html)")
	fs.StringVar(&o.Fonts, "fonts", "/usr/share/fonts/cf", "the directory of the font files the job names")
	fs.StringVar(&o.Job, "job", "", "the job file")
	fs.StringVar(&o.Out, "out", "", "the output directory for the frames and manifest.json")
	fs.StringVar(&o.Tmp, "tmp", os.TempDir(), "scratch space for the browser profile")
	fs.DurationVar(&o.FrameTimeout, "frame-timeout", 5*time.Second, "the longest one frame may take before it is drawn again (a frame takes a few ms)")
	fs.StringVar(&o.TestHang, "test-hang", "", "tests only: N:K makes the first K attempts at frame N hang")
	fs.DurationVar(&o.LoadTimeout, "load-timeout", 30*time.Second, "the longest the page load and mount may take")
	version := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *version {
		fmt.Println("cf-motion 1")
		return
	}
	if o.Bundle == "" || o.Job == "" || o.Out == "" {
		fmt.Fprintln(os.Stderr, "cf-motion: --bundle, --job and --out are required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintf(os.Stderr, "cf-motion: %v\n", err)
		stop()
		os.Exit(1)
	}
}
