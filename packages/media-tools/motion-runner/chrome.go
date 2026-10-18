package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// chromeFlags are the command line of chrome-headless-shell. They fall in four groups.
//
// Isolation. The browser speaks the DevTools protocol over a pipe only (no port), every network request goes to a proxy that
// does not exist and every host name resolves to nothing, so even a sandbox with a network would give the page none.
// Extensions, sync, component updates, crash reporting, background networking and every phone-home feature are off.
// --no-sandbox turns off Chrome's own process sandbox: it needs user namespaces or a setuid helper, which the container
// sandbox (no capabilities, no-new-privileges, gVisor) does not give it. The page is our own template code with the
// creator's words set as text; the container is the boundary (docs/design/motion-graphics-v1.md section 5).
//
// Determinism. Colour pinned to sRGB, no font hinting and no LCD text, every compositor stage run before a frame is drawn,
// no partial raster and no checker imaging (a frame is never drawn from stale tiles), animations on the main thread, the
// GPU off (Skia's software raster, the same on every machine of an architecture).
//
// Resources. Shared memory in /tmp (the container's /dev/shm is small), one renderer process, no zygote, a bounded V8 heap.
//
// Noise. No first-run, no default-browser check, no scrollbars, no audio.
func chromeFlags(userDataDir string, cssW, cssH int) []string {
	return []string{
		"--headless",
		"--remote-debugging-pipe",
		"--no-sandbox",
		"--no-zygote",
		"--user-data-dir=" + userDataDir,
		"--window-size=" + strconv.Itoa(cssW) + "," + strconv.Itoa(cssH),
		// Isolation.
		"--proxy-server=socks5://127.0.0.1:9",
		"--proxy-bypass-list=<-loopback>",
		"--host-resolver-rules=MAP * ~NOTFOUND",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-sync",
		"--disable-breakpad",
		"--disable-client-side-phishing-detection",
		"--disable-crash-reporter",
		"--no-pings",
		"--disable-features=Translate,OptimizationHints,MediaRouter,DialMediaRouteProvider,AutofillServerCommunication,CertificateTransparencyComponentUpdater,InterestFeedContentSuggestions,PrivacySandboxSettings4,NetworkTimeServiceQuerying,PaintHolding",
		"--disable-file-system",
		"--deny-permission-prompts",
		// Determinism.
		"--force-color-profile=srgb",
		"--font-render-hinting=none",
		"--disable-lcd-text",
		"--disable-gpu",
		"--disable-gpu-compositing",
		"--disable-partial-raster",
		"--disable-checker-imaging",
		"--run-all-compositor-stages-before-draw",
		"--disable-threaded-animation",
		"--disable-threaded-scrolling",
		"--disable-new-content-rendering-timeout",
		"--disable-frame-rate-limit",
		"--disable-gpu-vsync",
		"--disable-skia-runtime-opts",
		"--js-flags=--random-seed=1 --max-old-space-size=512",
		// Resources.
		"--disable-dev-shm-usage",
		"--renderer-process-limit=1",
		// Noise.
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-scrollbars",
		"--mute-audio",
		"--disable-logging",
		"--log-level=3",
		"about:blank",
	}
}

// browser is a running chrome-headless-shell and the protocol client on its pipe.
type browser struct {
	cmd    *exec.Cmd
	client *client
	stderr *tailBuffer
	once   sync.Once
}

// launchChrome starts the browser. Its working directory and HOME are a fresh directory under tmp, its environment holds
// nothing of ours, and it is killed with its whole process group by close.
func launchChrome(ctx context.Context, chrome, tmp string, cssW, cssH int) (*browser, error) {
	profile, err := os.MkdirTemp(tmp, "chrome-")
	if err != nil {
		return nil, fmt.Errorf("chrome: profile directory: %w", err)
	}
	// Chrome writes to fd 4 and reads from fd 3.
	fromChrome, chromeOut, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("chrome: pipe: %w", err)
	}
	chromeIn, toChrome, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("chrome: pipe: %w", err)
	}
	cmd := exec.CommandContext(ctx, chrome, chromeFlags(profile, cssW, cssH)...) // #nosec G204 -- our flags, the binary of the image
	cmd.Dir = profile
	cmd.Env = []string{"HOME=" + profile, "TMPDIR=" + tmp, "XDG_CONFIG_HOME=" + profile, "XDG_CACHE_HOME=" + profile, "FONTCONFIG_FILE=/etc/fonts/fonts.conf"}
	cmd.ExtraFiles = []*os.File{chromeIn, chromeOut}
	tb := &tailBuffer{max: 64 << 10}
	cmd.Stdout = tb
	cmd.Stderr = tb
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("chrome: start %s: %w", chrome, err)
	}
	// The child holds its own copies of these ends.
	_ = chromeIn.Close()
	_ = chromeOut.Close()
	b := &browser{cmd: cmd, client: newClient(fromChrome, toChrome), stderr: tb}
	go func() { _ = cmd.Wait(); _ = toChrome.Close(); _ = fromChrome.Close() }()
	return b, nil
}

// close asks the browser to quit, then kills its process group.
func (b *browser) close() {
	b.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.client.call(ctx, "", "Browser.close", nil, nil)
		if b.cmd.Process != nil {
			_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
		}
	})
}

// tailBuffer keeps the last max bytes written to it: Chrome's own log, for the error message when it dies.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := bytes.Split(bytes.TrimSpace(t.buf), []byte("\n"))
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}

// errChromeGone wraps a protocol failure that happened because the browser exited.
var errChromeGone = errors.New("chrome exited")
