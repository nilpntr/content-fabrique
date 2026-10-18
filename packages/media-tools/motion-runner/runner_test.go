package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/job.json is written by the worker's own test (apps/server/internal/motion TestJobMatchesRunnerContract): the runner must
// read what the worker writes, with its strict decoder.
func contractJob(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/job.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParsesTheWorkersJob(t *testing.T) {
	j, err := parseJob(contractJob(t))
	if err != nil {
		t.Fatal(err)
	}
	if j.Scale() != 1 || len(j.Items) != 8 || j.TotalFrames() <= 0 {
		t.Fatalf("job %+v", j)
	}
}

// mutate decodes the contract job, changes it and encodes it again.
func mutate(t *testing.T, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(contractJob(t), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func obj(v any) map[string]any              { m, _ := v.(map[string]any); return m }
func arr(v any) []any                       { a, _ := v.([]any); return a }
func item0(m map[string]any) map[string]any { return obj(arr(m["items"])[0]) }
func seg0(m map[string]any) map[string]any  { return obj(arr(m["segments"])[0]) }

func TestRefusesBadJobs(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"unknown field":     func(m map[string]any) { m["chrome_flags"] = []string{"--remote-debugging-port=9222"} },
		"version":           func(m map[string]any) { m["version"] = 2 },
		"fps":               func(m map[string]any) { m["fps"] = 1000 },
		"huge frame":        func(m map[string]any) { m["width"], m["css_width"] = 100000, 100000 },
		"frames bigger":     func(m map[string]any) { m["width"] = 2000 },
		"not one scale":     func(m map[string]any) { m["height"] = 700 },
		"no segments":       func(m map[string]any) { m["segments"] = []any{} },
		"too many frames":   func(m map[string]any) { seg0(m)["end_frame"] = 1_000_000 },
		"odd clip":          func(m map[string]any) { obj(seg0(m)["clip"])["x"] = 3 },
		"clip outside":      func(m map[string]any) { obj(seg0(m)["clip"])["w"] = 5000 },
		"font path":         func(m map[string]any) { obj(arr(m["fonts"])[0])["file"] = "../../etc/passwd.ttf" },
		"font family":       func(m map[string]any) { obj(arr(m["fonts"])[0])["family"] = `Anton"; src: url(x)` },
		"item id":           func(m map[string]any) { item0(m)["id"] = "G1<script>" },
		"template name":     func(m map[string]any) { item0(m)["template"] = "../evil" },
		"box outside":       func(m map[string]any) { obj(item0(m)["box"])["x"] = 5000 },
		"ink outside item":  func(m map[string]any) { item0(m)["ink_at_ms"] = []any{999999} },
		"params not object": func(m map[string]any) { item0(m)["params"] = "x" },
		"params too big": func(m map[string]any) {
			item0(m)["params"] = map[string]any{"lines": []string{strings.Repeat("A", 20000)}}
		},
		"duplicate item":      func(m map[string]any) { m["items"] = append(arr(m["items"]), item0(m)) },
		"item ends after max": func(m map[string]any) { item0(m)["end_ms"] = 10_000_000 },
	}
	for name, fn := range cases {
		if _, err := parseJob(mutate(t, fn)); err == nil {
			t.Errorf("%s: the job was accepted", name)
		}
	}
	if _, err := parseJob(append(contractJob(t), []byte(`{"version":1}`)...)); err == nil {
		t.Error("trailing data was accepted")
	}
}

func TestAllowedURLs(t *testing.T) {
	bundle := "/in/0"
	for url, want := range map[string]bool{
		"file:///in/0/scene.html":             true,
		"file:///in/0/templates/title_pop.js": true,
		"file://localhost/in/0/base.css":      true,
		"file:///in/0":                        false,
		"file:///in/01/x.js":                  false,
		"file:///in/0/../1/x":                 false,
		"file:///in/0/%2e%2e/1/x":             false,
		"file:///etc/passwd":                  false,
		"file://evil.example/in/0/scene.html": false,
		"file:///in/0/scene.html?x=1":         false,
		"http://example.com/x.js":             false,
		"https://in/0/scene.html":             false,
		"data:text/javascript,alert(1)":       false,
		"blob:file:///abc":                    false,
		"ws://127.0.0.1:9222":                 false,
		"chrome://version":                    false,
	} {
		if got := allowedURL(url, bundle); got != want {
			t.Errorf("allowedURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestChromeFlagsIsolate(t *testing.T) {
	flags := strings.Join(chromeFlags("/tmp/p", 1080, 1920), " ")
	for _, want := range []string{"--remote-debugging-pipe", "--proxy-server=socks5://127.0.0.1:9", "--host-resolver-rules=MAP * ~NOTFOUND",
		"--disable-extensions", "--disable-background-networking", "--disable-component-update", "--disable-gpu", "--force-color-profile=srgb"} {
		if !strings.Contains(flags, want) {
			t.Errorf("chrome flags lack %s", want)
		}
	}
	for _, bad := range []string{"--remote-debugging-port", "--allow-file-access-from-files", "--disable-web-security", "--enable-unsafe", "--remote-allow-origins"} {
		if strings.Contains(flags, bad) {
			t.Errorf("chrome flags contain %s", bad)
		}
	}
}

func TestReadMessage(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("{\"id\":1}\x00{\"method\":\"x\"}\x00partial"), 16)
	for _, want := range []string{`{"id":1}`, `{"method":"x"}`} {
		got, err := readMessage(br)
		if err != nil || string(got) != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := readMessage(br); err == nil {
		t.Error("an unterminated message was returned")
	}
	big := bufio.NewReader(io.LimitReader(repeatReader('a'), maxMessageBytes+10))
	if _, err := readMessage(big); err == nil || !strings.Contains(err.Error(), "over") {
		t.Errorf("an oversized message: %v", err)
	}
}

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

// The client matches answers to calls by id and routes events, over a pipe like Chrome's.
func TestClientCallsAndEvents(t *testing.T) {
	toChromeR, toChromeW := io.Pipe()
	fromChromeR, fromChromeW := io.Pipe()
	c := newClient(fromChromeR, toChromeW)
	go func() { // a fake Chrome: an event, then the answer to each command
		br := bufio.NewReader(toChromeR)
		for {
			raw, err := readMessage(br)
			if err != nil {
				return
			}
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(raw, &req)
			_, _ = fromChromeW.Write([]byte(`{"method":"Log.entryAdded","params":{}}` + "\x00"))
			if req.Method == "Fail.me" {
				_, _ = fromChromeW.Write([]byte(`{"id":` + itoa(req.ID) + `,"error":{"code":-32000,"message":"no"}}` + "\x00"))
				continue
			}
			_, _ = fromChromeW.Write([]byte(`{"id":` + itoa(req.ID) + `,"result":{"product":"HeadlessChrome/1"}}` + "\x00"))
		}
	}()
	go func() {
		for range c.events {
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var v struct {
		Product string `json:"product"`
	}
	if err := c.call(ctx, "", "Browser.getVersion", nil, &v); err != nil || v.Product != "HeadlessChrome/1" {
		t.Fatalf("%v %+v", err, v)
	}
	if err := c.call(ctx, "s1", "Fail.me", nil, nil); err == nil || !strings.Contains(err.Error(), "no") {
		t.Fatalf("an error answer: %v", err)
	}
	_ = fromChromeW.Close()
	if err := c.call(ctx, "", "Browser.getVersion", nil, nil); err == nil {
		t.Fatal("a call after Chrome closed the pipe succeeded")
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// pngOf encodes an NRGBA image of the clip's size with the given pixels painted.
func pngOf(w, h int, paint func(img *image.NRGBA)) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	paint(img)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestFrameChecks(t *testing.T) {
	j := &Job{Width: 540, Height: 960, CSSWidth: 1080, CSSHeight: 1920, FPS: 30,
		Items: []Item{{ID: "g1", Template: "title_pop", StartMs: 0, EndMs: 2000, Box: Box{X: 100, Y: 300, W: 400, H: 100}}}}
	clip := PixelRect{X: 30, Y: 130, W: 240, H: 80}
	checks := []inkCheck{{item: j.Items[0]}}
	// The box in frame pixels is x 50..250, y 150..200; in the clip that is x 20..220, y 20..70.
	ink := pngOf(240, 80, func(img *image.NRGBA) {
		for y := 30; y < 60; y++ {
			for x := 40; x < 200; x++ {
				img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			}
		}
	})
	if err := checkFrame(ink, j, checks, 30, clip); err != nil {
		t.Errorf("a frame with ink in the box: %v", err)
	}
	blank := pngOf(240, 80, func(*image.NRGBA) {})
	if err := checkFrame(blank, j, checks, 30, clip); err == nil || !strings.Contains(err.Error(), "blank") {
		t.Errorf("a blank frame: %v", err)
	}
	if err := checkFrame(blank, j, nil, 30, clip); err != nil {
		t.Errorf("a blank frame with no ink check: %v", err)
	}
	// A template that paints its whole clip (a background) spills outside its box.
	opaque := pngOf(240, 80, func(img *image.NRGBA) {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	})
	if err := checkFrame(opaque, j, nil, 30, PixelRect{X: 0, Y: 0, W: 240, H: 80}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("a frame painted outside the box: %v", err)
	}
	if err := checkFrame([]byte("not a png"), j, checks, 30, clip); err == nil {
		t.Error("a capture that is not a PNG was accepted")
	}
	if err := checkFrame(pngOf(100, 80, func(*image.NRGBA) {}), j, nil, 30, clip); err == nil {
		t.Error("a capture of the wrong size was accepted")
	}
}
