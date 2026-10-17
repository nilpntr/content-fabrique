package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const sfxUsage = `Usage:
  cfctl sfx gallery -o <page.html> [--provider local|docker|hardened] [--image media-tools:dev] [--gain <dB>]

DEVELOPMENT ONLY. gallery renders every sound effect category and variant of the renderer (internal/render, P3-11) with the
same graph the renders use, and writes a small static web page you open in a browser to listen: a player for every sample, its
waveform (ffmpeg showwavespic) and its spectrogram (ffmpeg showspectrumpic), plus three round-robin items per category (the
"auto" variant with different ids: the same sound with a small jitter of pitch, length and filter). The page is -o, its files go
in a directory next to it named <page>_files. Nothing is written into the repository: the command refuses an output path
inside it.

  -o          the page to write (required, a path outside the repository; replaced if it exists)
  --provider  where ffmpeg runs: local (default: ffmpeg on this machine, no isolation, development only), docker or hardened
  --image     the media-tools image for docker and hardened (default media-tools:dev)
  --docker    the docker CLI (default docker)
  --gain      gain_db of every sample, -30 to 6 (default 0)
`

func cmdSFX(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, sfxUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, sfxUsage)
		return 0
	case "gallery":
		return cmdSFXGallery(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl sfx: unknown command %q\n\n%s", args[0], sfxUsage)
	return 2
}

// galleryRepoRoot finds the repository the working directory is in (the same root the env commands use), or "" outside one.
func galleryRepoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	root, err := envfile.FindRoot(cwd)
	if err != nil {
		return ""
	}
	return root
}

// outsideRepo reports whether path (absolute) is outside the repository root, resolving links in the part that exists.
func outsideRepo(path, root string) bool {
	if root == "" {
		return true
	}
	resolve := func(p string) string {
		for q := p; ; q = filepath.Dir(q) {
			if r, err := filepath.EvalSymlinks(q); err == nil {
				return filepath.Join(r, strings.TrimPrefix(p, q))
			}
			if filepath.Dir(q) == q {
				return p
			}
		}
	}
	rel, err := filepath.Rel(resolve(root), resolve(path))
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// galleryEntry is one sample of the page.
type galleryEntry struct {
	Category, Variant, ID string
	Auto                  bool
	Resolved              string
	Ms                    int64
	Base                  string // file name without extension
	Err                   error
}

func cmdSFXGallery(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sfx gallery", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, sfxUsage) }
	outArg := flags.String("o", "", "the page to write")
	providerArg := flags.String("provider", "local", "local, docker or hardened")
	imageArg := flags.String("image", "media-tools:dev", "media-tools image")
	dockerArg := flags.String("docker", "docker", "docker CLI")
	gainArg := flags.Float64("gain", 0, "gain_db of every sample")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *outArg == "" || flags.NArg() != 0 {
		_, _ = fmt.Fprint(stderr, sfxUsage)
		return 2
	}
	page, err := filepath.Abs(*outArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %v\n", err)
		return 2
	}
	if !outsideRepo(page, galleryRepoRoot()) {
		_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %s is inside the repository: the gallery is written outside it (try /tmp/cf-sfx/index.html)\n", page)
		return 2
	}
	if *providerArg == "local" {
		env, _ := os.LookupEnv("CF_ENV")
		if err := sandbox.CheckLocalAllowed(env); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %v\n", err)
			return 2
		}
		_, _ = fmt.Fprintln(stderr, "cfctl: the local provider runs ffmpeg on this machine without isolation: development only")
	}
	filesName := strings.TrimSuffix(filepath.Base(page), filepath.Ext(page)) + "_files"
	filesDir := filepath.Join(filepath.Dir(page), filesName)
	if err := os.MkdirAll(filesDir, 0o750); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %v\n", err)
		return 1
	}
	if filesDir, err = filepath.EvalSymlinks(filesDir); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %v\n", err)
		return 1
	}
	runner, err := newGalleryRunner(*providerArg, *dockerArg, *imageArg, filesDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %v\n", err)
		return 2
	}

	entries := galleryEntries()
	ctx, stop := signalContext()
	defer stop()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(e *galleryEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			e.Err = renderGallerySample(ctx, runner, filesDir, e, *gainArg)
		}(&entries[i])
	}
	wg.Wait()
	failed := 0
	for _, e := range entries {
		if e.Err != nil {
			failed++
			_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %s/%s (%s): %v\n", e.Category, e.Variant, e.ID, e.Err)
		}
	}
	if err := os.WriteFile(page, []byte(galleryHTML(entries, filesName, *gainArg)), 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl sfx gallery: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s (%d samples, %d failed) and %s/\n", page, len(entries)-failed, failed, filesDir)
	if failed > 0 {
		return 1
	}
	return 0
}

// galleryEntries lists what the page shows: every variant with the id "s1", then three round-robin items per category.
func galleryEntries() []galleryEntry {
	var out []galleryEntry
	for _, c := range timeline.SFXCategories() {
		vs, _ := timeline.SFXVariants(c)
		for _, v := range vs {
			out = append(out, galleryEntry{Category: c, Variant: v, ID: "s1"})
		}
	}
	for _, c := range timeline.SFXCategories() {
		for _, id := range []string{"rr1", "rr2", "rr3"} {
			out = append(out, galleryEntry{Category: c, Variant: timeline.SFXVariantAuto, ID: id, Auto: true})
		}
	}
	return out
}

func newGalleryRunner(name, dockerBin, image, dir string) (media.Runner, error) {
	pol := sandbox.Policy{AllowedRoots: []string{dir}}
	var p sandbox.Provider
	switch name {
	case "local":
		p = sandbox.NewLocal(pol)
	case "docker":
		d := sandbox.NewDocker(pol)
		d.Binary = dockerBin
		p = d
	case "hardened":
		h := sandbox.NewHardened(pol)
		h.Binary = dockerBin
		p = h
	default:
		return nil, fmt.Errorf("unknown provider %q (use local, docker or hardened)", name)
	}
	return &media.SandboxRunner{Provider: p, Image: image, Limits: render.DefaultLimits(), Labels: map[string]string{"kind": "sfx_gallery"}}, nil
}

// renderGallerySample renders one sample with the graph the renders use, in one ffmpeg run: the wav, its waveform and its
// spectrogram. The category and variant are the catalogue's own, so nothing here comes from outside.
func renderGallerySample(ctx context.Context, runner media.Runner, dir string, e *galleryEntry, gainDB float64) error {
	graph, ms, err := render.SFXPreviewGraph(e.Category, e.Variant, e.ID, gainDB)
	if err != nil {
		return err
	}
	e.Ms = ms
	e.Resolved = e.Variant
	if e.Auto {
		e.Resolved, _ = timeline.ResolveSFXVariant(e.Category, e.Variant, e.ID)
	}
	e.Base = fmt.Sprintf("%s_%s_%s", e.Category, e.Variant, e.ID)
	// The preview graph ends in [aout]: split it three ways, the sound, its waveform and its spectrogram.
	full := graph + ";[aout]asplit=3[wav][w][s];" +
		"[w]showwavespic=s=640x120:colors=0x78aaff:split_channels=0[wp];" +
		"[s]showspectrumpic=s=640x240:legend=0:fscale=log:color=intensity[sp]"
	args := []string{
		"ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-filter_complex", full,
		"-map", "[wav]", "-c:a", "pcm_s16le", media.OutputPath(e.Base + ".wav"),
		"-map", "[wp]", "-frames:v", "1", media.OutputPath(e.Base + ".wave.png"),
		"-map", "[sp]", "-frames:v", "1", media.OutputPath(e.Base + ".spec.png"),
	}
	res, err := runner.Run(ctx, media.Invocation{Args: args, OutputDir: dir})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	return nil
}

func galleryHTML(entries []galleryEntry, filesName string, gainDB float64) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Content Fabrique sound effects</title>
<style>
body{background:#14141a;color:#dcdce6;font:14px/1.4 system-ui,sans-serif;margin:24px auto;max-width:1100px;padding:0 16px}
h1{font-size:22px}h2{font-size:17px;margin:28px 0 6px;border-bottom:1px solid #333;padding-bottom:4px}
.note{color:#9a9aaa;max-width:80ch}.row{display:flex;gap:14px;align-items:flex-start;margin:10px 0}.meta{width:170px;flex:none}
.meta b{display:block;color:#fff}.meta span{color:#9a9aaa}img{display:block;background:#000;max-width:100%}
audio{width:260px;margin-top:6px}.imgs{display:flex;flex-direction:column;gap:2px}.err{color:#ff6b6b}
</style></head><body><h1>Sound effects</h1>
<p class="note">Development only: generated by <code>cfctl sfx gallery</code> from the renderer's own graph (internal/render, P3-11), at the level they are mixed at (base -6 dB`)
	fmt.Fprintf(&b, " plus gain %s dB", html.EscapeString(fmt.Sprint(gainDB)))
	b.WriteString(`, then the limiter). Each row is one sample: the player, its waveform, its spectrogram (log frequency). The round-robin rows are the <code>auto</code> variant with different item ids: the same family with a small, deterministic jitter of pitch, length and filter. Listen on laptop speakers and on headphones, and against music.</p>`)
	cat := ""
	for _, e := range entries {
		if e.Category != cat {
			cat = e.Category
			fmt.Fprintf(&b, "<h2>%s</h2>\n", html.EscapeString(cat))
		}
		title := e.Variant
		if e.Auto {
			title = "auto (" + e.ID + ") = " + e.Resolved
		}
		fmt.Fprintf(&b, `<div class="row"><div class="meta"><b>%s / %s</b><span>%d ms, id %s</span>`, html.EscapeString(e.Category), html.EscapeString(title), e.Ms, html.EscapeString(e.ID))
		if e.Err != nil {
			fmt.Fprintf(&b, `<span class="err">failed: %s</span></div></div>`+"\n", html.EscapeString(e.Err.Error()))
			continue
		}
		fmt.Fprintf(&b, `<audio controls preload="none" src="%s/%s.wav"></audio></div><div class="imgs"><img width="640" height="120" alt="waveform" src="%s/%s.wave.png"><img width="640" height="240" alt="spectrogram" src="%s/%s.spec.png"></div></div>`+"\n",
			filesName, e.Base, filesName, e.Base, filesName, e.Base)
	}
	b.WriteString("</body></html>\n")
	return b.String()
}
