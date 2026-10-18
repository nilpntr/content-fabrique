# Design note: motion graphics v1 (templates rendered by headless Chrome)

> Status: **proposed, built in two PRs** · 2026-10-18 · Decision [D-35](../05-decisions.md#d-35-motion-graphics-are-html-templates-rendered-by-headless-chrome-in-the-media-sandbox). The engine (this note's sections 1 to 8) is the first PR; the editor's use of it (section 9) is the second. It extends [timeline-v1](timeline-v1.md), [edit-style-v1](edit-style-v1.md) and [style-profile-v1](style-profile-v1.md), and changes the "how it renders" column of [D-26](../05-decisions.md#d-26-video-effects-are-presets) for graphic templates. The director runs a security review of section 5 before it ships.

## 1. Why

The founder, after the first edits with the creator's style: the edits feel static, with "no visual effects on entering the text in the screen". Titles were ASS subtitles with `fade`, `pop_in`, `slide_up` and `typewriter`; P6-06 (D-34) added a `pop` entrance and exits to them. That is still one block of type that appears and leaves. The references for the feel are herk-edit-kit's `MOTION_PHILOSOPHY.md` (type is a character: word-by-word reveals with scale and blur, motion blur on movement, something new every 0.3 to 0.6 s, holds long enough to read) and open-edit's renderer (HTML compositions rendered in headless Chrome on a virtual clock, frame-exact, with transparency).

What we want is designer-grade type motion (per-word overshoot, blur-in, masked wipes, a marker swipe, a number that counts, letters that drop) that lands on the beat, that the editor can choose and parameterise, and that renders deterministically inside the media sandbox.

## 2. Templates, not model-written HTML

The editor never writes HTML, CSS or JavaScript. It writes a **graphics item** on the timeline: a template from a versioned library and its params, validated by the timeline schema like every other field (D-02). The library is ours, reviewed, golden-tested and versioned with the server.

| Template | What it does | Accents land | ASS fallback (entrance, exit) |
|---|---|---|---|
| `title_pop` | word by word: each word scales up from 0.55 with a blur, overshoots and settles; then floats and pulses on the beat; leaves last word first | word *i* (its overshoot peak) on accent *i*; the accents after the last word pulse the title | `pop`, `pop_out` |
| `blur_in` | the title sharpens out of a blur while it drifts into place, line by line; no overshoot | line *i* sharp on accent *i* | `fade`, `fade` |
| `chapter_card` | "1. SHRUGS VOOR UPPER TRAPS": the number pops first in the accent colour with a small turn, then the line sweeps in word by word from behind a hard edge; `two_line` puts the number on its own line | number on the first, sweep on the second | `slide_up`, `slide_down` |
| `highlight_word` | the line rises in, then a slanted marker in the accent colour swipes in behind one word, which turns to the colour that reads on it | marker on the first | `fade`, `fade` |
| `counter` | "0 → 12 REPS": tabular digits count with an ease-out and land with a pop; the label slides in next to them | the count lands on the first | `pop`, `pop_out` |
| `lower_label` | a small label on a plate: an accent stripe drops in, the plate wipes out from it, the words rise in from below its edge | it enters on the first | `slide_up`, `slide_down` |
| `outro_card` | the loop card: letters drop in with a blur, an accent line grows from the centre, the card breathes, and it pushes out and blurs on the last frame so the video loops | the letters start on the first | `fade`, `pop_out` |

Every template has an exit (the words leave, the plate wipes back, the card pushes out) that ends with the item.

**Params** (the schema is the contract; `packages/schemas/timeline/v1.schema.json`, `$defs/graphicsParams*`): `text` (1 to 60 characters, at most 2 lines), `style` (a style id of the registry: the creator's title style from the style profile, so the font, size, colour and shadow follow the creator; the spec still cannot name a font), `fill`, `accent` (`#RRGGBB`), `scale` (0.6 to 1.4), `align`, `shadow`, `backing` (`plate` for a translucent plate behind busy footage), `energy` (`calm`, `lively`, `hype`) and `accents_ms` (timeline times, usually beats). Per template: `number` and `layout` (chapter_card), `word` (highlight_word), `from`, `to`, `decimals` (counter). A param the template does not take is a schema error, and `invalid_graphic` for a spec built in Go.

**The box.** An item is laid out as its text in its style (`GraphicsItem.AsText`: the chapter card's "1. TITLE", the counter's widest value and label), with the fonts' real advance widths, exactly like a text item (`timeline.PlaceTextAvoiding`). So it is placed by the same solver: inside the safe zone, wrapped at the text column, kept off the subject (above the head, smaller, or below the person) and never drawn over the body: where there is no room at its own time it is shortened, moved to the nearest time with room (its accents move with it) or left out with a warning, exactly as P6-06 does for text (`subject.FitText`), and kept clear of the caption band. The validator checks it with the text checks (`unsafe_text`, `text_overlaps_captions`, `unsupported_character`, `outside_timeline`). The template draws into that box: Go passes the laid-out lines, the font size (the em), the line height and the padding, and the page renders the lines with kerning, ligatures and synthesis off, so Chrome's line widths are the ones Go measured.

**Energy** is the feel of the motion (`internal/motion/timing.go`; sizes in layout pixels):

| Energy | Element in | Stagger | Overshoot | Blur in | Float while holding | Exit |
|---|---|---|---|---|---|---|
| calm | 560 ms | 300 ms | 1.03 | 8 px | none | 420 ms |
| lively (default) | 440 ms | 170 ms | 1.07 | 12 px | 3 px | 320 ms |
| hype | 340 ms | 90 ms | 1.12 | 16 px | 4 px | 240 ms |

The staggers are inside the 0.08 to 0.35 s of a word-by-word reveal. **Timing is computed in Go** (`motion.Schedule`): when each element enters, when everything is in (`FullyInMs`), when the exit starts. An accent places an entrance so that its peak lands on the beat (a `title_pop` word reaches its overshoot at 60 % of its entrance, so it starts 0.6 x 440 ms before the beat). The check that a title stays readable reads the same numbers (section 9).

**Versions.** Each template registers a version (all `1`). The bundle has a hash (`motion.BundleHash`, over every file); a plan records the hash it was compiled against and `Execute` refuses a plan from another bundle. A change to any template changes the golden hashes (section 7), so the change is reviewed frame by frame. A breaking change to a template's look is a new template name or version, not an edit.

## 3. The render path

```
spec ── Compile ─────────────────────────────────────────────── Plan
          │ graphics(): Resolve each item (box, lines, motion, times) │ Motion: the job (items, segments, clips, fonts)
          │ BuildJob: frame ranges merged into segments,          │ Args: ... -f image2 -i motion/s<k>/f%06d.png (one per segment)
          │           each with a clip (union of its boxes + 2 %) │ Graph: [joined clips] overlay(s) [scale for preview] → ass → [vout]
          ▼
Execute ── runMotion: stage the bundle (read-only mount), write motion-job.json into the work dir
          │          sandbox job: cf-motion --bundle /in/0 --job /work/motion-job.json --out /work/motion
          │          read manifest.json (untrusted: regular files, size caps, SHA-256 of every frame)
          └── ffmpeg (same sandbox, same work dir) reads the PNG sequences and composites them
```

- **cf-motion** (`packages/media-tools/motion-runner`, Go standard library only, built statically into the image) starts chrome-headless-shell with the DevTools protocol on a pipe (file descriptors 3 and 4; no port), opens `scene.html` from the bundle, sets the viewport to the output size with a transparent background, loads the fonts as bytes, mounts the items and, for every frame of every segment, seeks the page and captures the segment's clip as a PNG.
- **The virtual clock** (`bundle/runtime.js`, our own code; the idea follows open-edit's page runtime): `performance.now`, `Date`, `requestAnimationFrame` and the timers only move when the runner seeks, and `Math.random` is a seeded xorshift. A seek pauses every animation of every item on screen and sets its `currentTime` to the item's local time; ticks (the counter's number) run as a function of that time; items off screen are hidden. Templates animate with the Web Animations API (`element.animate`) only.
- **Only active ranges are rendered.** The frames of an item's `[at_ms, at_ms + duration_ms)` are merged into segments (touching ranges become one, at most 64), and only the segment's clip is captured: a title costs a strip, not a frame.
- **Frames at scale 1.** The job is always the output's own size (1080 wide). Measured: at a device scale factor of 0.5 (the preview's size) Chrome's software raster is not deterministic from run to run (frames of animated text differ between two renders), at 1 it is. So the preview composites the same frames as the final, scaled down by ffmpeg (`scale=...:flags=area`). The runner refuses any other scale.
- **Output: a PNG sequence with straight alpha.** Chrome's capture is already a PNG; writing it is free, its bytes are hashable as they come, the alpha is exact and straight (what ffmpeg's `overlay` expects), and nothing is encoded twice. ProRes 4444 (open-edit's choice) adds an encode and a decode per frame and is lossy 10-bit; VP9 with alpha needs libvpx, which our ffmpeg does not build (`--disable-autodetect`, D-16). The cost is disk: about 10 to 40 kB per clipped frame, 20 MB for 20 s of overlays at 1080 wide, in the job's work directory, gone with it.
- **Composition.** Each segment is an `image2` input after the clips and audio, shifted to its first frame's time (`setpts=...+start/TB`) and laid over the joined clips at its clip's corner with `eof_action=pass`, so before and after a segment the picture passes through. The overlays sit under the ASS script: captions and the preview watermark stay on top. The time map lists the overlays like text items.
- **Fail loudly.** The run fails, and so the render, on: a job that does not parse or is out of bounds; a page error, an uncaught exception, a `console.error`, a failed load, a refused request, a font that did not load, a crash; an item that is blank at a time it should be on screen (its box must have ink at half opacity on at least 0.4 % of its pixels, checked when it is fully in and half-way through its hold); ink drawn outside every item's box plus the 2 % margin (a template that painted the background); a manifest that does not match the job; a frame whose bytes do not match its hash. A missing title is worse than a failed render, which is retried or switched to the fallback (section 6).

## 4. Determinism

The same spec gives the same frames, byte for byte, on one platform (architecture and Chrome version):
- the virtual clock and seeded randomness (section 3); no network, no real time, no data from outside the job;
- Chrome flags: `--force-color-profile=srgb`, `--font-render-hinting=none`, `--disable-lcd-text`, the GPU off (Skia's software raster), `--disable-skia-runtime-opts` (no CPU-specific Skia paths, so two x86-64 machines with different vector units draw the same), `--disable-partial-raster`, `--disable-checker-imaging`, `--run-all-compositor-stages-before-draw`, `--disable-threaded-animation`, and the frame-rate limit off (a capture does not wait for a vsync: 4 to 6 ms a frame instead of 49);
- fonts loaded as bytes from the image's vendored OFL files (never the system's), kerning and ligatures off;
- a device scale factor of 1 (section 3).

**Tested by hashing** (`internal/motion/integration_test.go`): every template renders twice to identical frames, and the frames' hashes match `testdata/golden-hashes.json`, which holds one set per platform (`amd64/HeadlessChrome/155.0.8059.84`, `arm64/...`). CI requires the entry of its platform (`CF_MOTION_REQUIRE_GOLDEN`). The amd64 set was recorded under emulation on Apple silicon; if CI's native x86-64 disagrees, the test prints the platform's hashes to add after a look at the frames.

## 5. The sandbox and the security model

**What runs where.** Chrome and cf-motion run in the media-tools image, through the same `sandbox.Provider` as ffmpeg (docker locally, `hardened` = docker under gVisor in staging and production): no network (`--network none`), a read-only root, no capabilities, `no-new-privileges`, a non-root uid, a tmpfs `/tmp` (noexec), the bundle mounted read-only at `/in/0`, the work directory the only writable mount, limits of 4 CPUs, 3 to 4 GiB, 1024 PIDs and a timeout. The image is `FROM scratch`: no shell, no package manager.

**What the page can do.** The page is our template code; the only data in it is the item's params: the laid-out lines and a few numbers, colours and enum values that Go produced from the validated spec.
- Text reaches the DOM only through `textContent` (a source test fails on `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `eval`, `new Function`, `import()`, `fetch`, `XMLHttpRequest`, `WebSocket`, `setAttribute('style'...)`, `.src =` and `url(` in any bundle file).
- Colours are written by Go as `#RRGGBB` or `rgba()` from numbers and checked again by a strict pattern in the runtime; sizes are finite numbers in a range; font families come from the registry and are matched by a pattern in the runner.
- Arguments reach the page as JSON values of `Runtime.callFunctionOn`, never spliced into code.
- The scene's CSP is `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; child-src 'none'; manifest-src 'none'; base-uri 'none'; form-action 'none'`.
- The runtime removes `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`, workers and `window.open` from the page (defence in depth; the next two layers do not rely on it).

**What the browser can reach.** Every request of the page is paused by the runner (DevTools `Fetch` domain, `file:` included) and allowed only when it is a clean `file:` path inside the bundle directory: no query, no fragment, no `..`, no host. Anything else (http, https, ws, data, blob, a file outside the bundle) is failed and fails the run. Fonts are not loaded by URL at all. Under that: a proxy that does not exist (`--proxy-server=socks5://127.0.0.1:9`) and a resolver that resolves nothing (`--host-resolver-rules=MAP * ~NOTFOUND`), so even a sandbox with a network would give the page none; and under that, the sandbox's `--network none`. The DevTools protocol is on a pipe, so nothing in the container can connect to the browser either.

**Chrome's own sandbox is off** (`--no-sandbox`): it needs user namespaces or a setuid helper, which a container with no capabilities and `no-new-privileges` (and gVisor) does not give it. The renderer process is therefore confined by the container and gVisor only, not by Chrome's seccomp-bpf layer. This is acceptable because the page is our own code and the creator's words are text (section 5, above): there is no attacker-controlled script, and a Chrome renderer bug would need attacker-controlled content to be triggered. It is the main item for the security review.

**Hostile pages are tested** (`TestIntegrationHostilePagesInDocker`, also under runsc in CI): pages with no CSP that fetch `http://example.com/`, load an image from an IP address, open a WebSocket to the DevTools port, fetch, XHR, iframe and script-load `/etc/passwd` or `/etc/hostname`. Each run fails with the refusal in its error and nothing of the files reaches the output.

**Outputs are untrusted.** The worker reads the manifest and frames from the work directory as untrusted output: regular files only (no symlinks), size caps (4 MB manifest, 32 MB a frame), exactly the segments and frame counts the job asked for, and the SHA-256 of every frame checked against the manifest. ffmpeg decodes the PNGs in the same sandbox it decodes uploads in.

**Attack surface added by this change** (for the review):

| Surface | Exposure | Mitigation |
|---|---|---|
| chrome-headless-shell 155 (renderer, Skia, V8, Blink) | runs our pages only; creator text as `textContent` | container + gVisor, no network, no Chrome sandbox (above); pinned version, bumped with Chrome stable (a version bump is a reviewed PR with new goldens) |
| The page's params (creator text in `lines`) | text set with `textContent`; length-capped by the schema (60) and the runner (16 kB of params) | no markup path, CSP, source test |
| The job file (written by the worker) | parsed by cf-motion with unknown fields refused | every bound checked before Chrome starts (sizes, frames, items, names by pattern, times, boxes, clips) |
| The DevTools protocol | pipe only; messages capped at 64 MB | no port; `--remote-debugging-port` is refused by a test |
| Requests from the page | none needed | `Fetch` allowlist, CSP, dead proxy and resolver, `--network none` |
| Files the page can read | the bundle | allowlist on `file:` paths; the only mounts are the bundle (ro) and the work directory |
| Frames and manifest back to the worker | untrusted | regular files, caps, hashes, exact shape |
| The image (supply chain) | Chrome for Testing zip; 28 more Debian libraries; the golang image for the build | zip pinned by version and SHA-256 per architecture; Debian snapshot pinned by date; golang image by digest; licence allowlist (below) |
| Resource use | Chrome processes and memory | the job's CPU, memory, PID and time limits; one renderer process (`--renderer-process-limit=1`), V8 heap capped at 512 MB, shared memory in `/tmp` |

**Licences** (D-16, `packages/media-tools/LICENSES.md`): chrome-headless-shell is a named exception (Chromium is BSD-3-Clause; some components built into it are LGPL; it runs as a separate process in our container and is never distributed). Its `ABOUT` file refers to Google's terms of service for Chrome; whether that binds Chrome for Testing's headless shell used server-side is a question for the legal review (P0-12). The fallback, behind the same runner, is Debian's `chromium-headless-shell` package (built from source by Debian, DEP-5 copyright). The new Debian libraries are MIT, LGPL-2.1+ (dynamically loaded, as for ffmpeg's) and MPL-2.0 (NSS, NSPR), each with a reason in `policy.json`. No GSAP, no Node, no npm.

## 6. How it composes with the ASS text

- **ASS text stays.** Text items, captions and the preview watermark are unchanged and drawn above the overlays.
- **The fallback.** Every graphics item has a text form (`AsText`: its words, in its box, in its style, with the template's fallback animation). `Profile.MotionFallback` compiles the graphics track to those text items instead of overlays; the render jobs set it when `MOTION_GRAPHICS=off`, the operator's switch when the overlay renderer misbehaves (a bad Chrome, an image without it). A render does not fall back on its own after a failure: it fails loudly and is retried or rendered again with the switch.
- **Validation is the same for both**, because the box is the same: what the validator accepts, both paths can draw in the same place.
- **The editor** keeps using text items for quiet, static text (a creator whose style is plain titles) and uses graphics items where the motion is the point (section 9).

## 7. Tests

- `internal/timeline`: the graphics track decodes strictly and round-trips (fixture `v50_graphics_9x16`, all seven templates); invalid fixtures `i77` to `i84` (unknown template, a param of another template, a counter without `to`, an accent outside the item, a highlighted word that does not exist, an unsafe box, an item past the end, two graphics tracks); the TypeScript types are regenerated and Ajv checks the schema half.
- `internal/motion`: the catalog, bundle and schema agree; the bundle source rules (CSP, no markup or network APIs); `title_pop` lands each word's peak on its accent; staggers in range; every template enters before it leaves; the box is the text item's box; overlays keep off the head; segments merge and clip on even pixels; the job is deterministic; the job matches the runner's contract (`packages/media-tools/motion-runner/testdata/job.json`, written by this test, parsed by the runner's strict decoder in its own test).
- `packages/media-tools/motion-runner`: bad jobs are refused (20 cases, from unknown fields and a path in a font name to a clip outside the frame); the request allowlist (16 URLs); the DevTools pipe framing and size cap; the client's calls, errors and closed pipe; the blank, ink and spill checks on synthetic PNGs; the Chrome flags (pipe, proxy, resolver; never a debugging port, `--allow-file-access-from-files` or `--disable-web-security`).
- `internal/render`: the graphics fixture compiles to overlays (inputs after the others, before the ASS script, scaled in the preview, in the time map); the fallback draws the same words in the same boxes; a plan from another bundle is refused.
- **Against the image** (CI's media-tools job; gVisor in the gvisor-bench job): every template twice to identical bytes and to the platform's golden hashes; ink only inside its box on every frame; the hostile pages; the graphics fixture rendered end to end in both profiles.
- The media-tools image build checks Chrome and cf-motion start in the scratch root, and the licence check covers the new components on both architectures.

## 8. Performance

Measured on a Mac (Apple M4 Pro, Docker with 14 vCPUs), docker provider, the demo edit of section 10 (30 s, 9:16, five clips, seven overlays covering 20 s: 602 overlay frames), three runs each:

| | Overlay render (cf-motion) | Whole render with overlays | Whole render, ASS fallback |
|---|---|---|---|
| Preview (540x960) | 3.6 to 3.8 s | 7.0 to 7.7 s | 2.7 s |
| Final (1080x1920, from 4K originals) | 2.8 s | 31.6 to 31.9 s (one run 40.5) | 28.1 to 32.0 s |

Per frame: 4 to 6 ms for a title-sized clip, about 9 ms for a clip spanning most of the frame (the fixture's eight stacked items). Chrome starts in 20 to 60 ms and the page loads in 50 to 100 ms. In the preview the overlays add about 4.5 s, a third of it the compositing of full-size frames scaled down; in the final they are within the run-to-run noise of the encode.

**Budgets:** the overlay run of a 30 s edit stays under 6 s on the target host class and a preview with overlays under 10 s (the P3-06 budget); a job with overlays over 60 s of frames is a sign of a misuse (overlays on the whole edit) and still bounded by the job's frame limit (10,800) and timeout. CI logs the times of the integration tests; the target host (P0-10) does not exist yet.

Not done, because it was not needed: parallel tabs (Chrome renders one frame at a time; four tabs would cut the overlay time about in four on a 4-CPU job), and skipping identical hold frames.

## 9. The editor (second PR, `editor.v2.8`, `workout.v5`)

- `list_motion_templates`: the library (name, description, when to use, params, what the accents do, an example op) and, with `template`, that template's still preview (`internal/motion/previews/*.png`, rendered by the integration test, served as a built-in image so it never touches the object store).
- Ops `add_graphic`, `update_graphic` and `remove_graphic` (ids `g1`, `g2`, ...). A graphic keeps its absolute time while clips ripple, like a text, and `update_graphic` moving `at_ms` moves its accents with it.
- The prompt ("Motion graphics (lively and hype)", behind `op:add_graphic`) and the workout playbook: at lively or hype the hook title is a `title_pop` with its words on the first beats and each chapter a `chapter_card` whose number pops on its cut and whose line sweeps in on the next beat; calm keeps `blur_in` or plain text; the creator's title recipe goes into the params. A graphic enters and leaves by itself, so `text_without_entrance` does not apply to it.
- `check_edit`: `overlay_readable` (at least 1.2 s fully on screen before the exit, from `motion.Schedule`), `overlay_over_subject` (the renderer's `subject.FitText`: a graphic left out, a hook or chapter graphic moved to another time, or one pinned on the person fails) and `overlay_legibility` (the contrast of the letters against the picture behind their box, measured on the preview on a frame of the same shot just before or after the graphic, where it is not drawn; `media.RegionStatsArgs` measures up to 32 boxes in one sandboxed ffmpeg run). `hook_title` and `chapter_titles` count graphics.
- Not run live: the cassettes were re-recorded against the fake API (no paid calls). A live run is the founder's call.

## 10. The demo and its validation

`cfctl render <spec> --provider docker --image media-tools:dev [--subjects boxes.json] [--motion-fallback]` renders a spec with motion graphics through the sandbox and prints the overlay run's time. The demo (the creator's footage, local only, never committed; outputs in `/tmp`) is a 30 s back-day edit on a 124 BPM grid: a `title_pop` "BACK DAY 101" with its words on beats 1 to 3 and pulses on 5 and 7, `chapter_card`s 1 to 3 ("SEATED ROW", "LAT PULLDOWN", "MACHINE ROW") popping on each exercise's first beat, a `counter` to 12 REPS landing on a beat, a `highlight_word` "SQUEEZE AT THE TOP" and an `outro_card` over the hook's frame. The renderer warns, correctly, that two overlays touch the raised arms of the lat pulldown: the lifter's hands reach the top band of the frame at the top of each rep.

## 11. Alternatives considered

- **Let the model write HTML/CSS/JS per edit.** Rejected (D-26 already said so for effects): unvalidated, not golden-testable, a script from a model in the browser is the attack surface this design avoids, and taste is better encoded once by us than re-derived per run. Bespoke effects stay behind the flagged sandbox escape hatch (P4-08).
- **More ASS.** libass has `\blur`, `\t` and `\clip`, so a blur-in or a scale could be approximated per word. Rejected as the main path: no masks or plates that animate independently, no tabular counting, easing limited to `\t`'s acceleration, and per-word layout would be ours to emulate. It is the fallback.
- **GSAP.** Its licence is a custom no-charge licence, not an OSI one, with a clause about products that compete with Webflow; an AI video editor is plausibly outside it but it would need a legal read, and the Web Animations API covers everything these templates do. Not added.
- **Remotion or another React renderer.** Its licence requires a company licence for companies of more than three people; it would bring Node and npm into the media image.
- **HyperFrames** (named by D-26). Its runtime brings its own composition model and Node tooling; we needed a small, auditable runtime that seeks WAAPI animations on a virtual clock and nothing else, so we wrote one (and keep the template idea).
- **Puppeteer or Playwright as the driver.** A Node runtime and its dependency tree in the image, for what is a few hundred lines of DevTools calls; the Go runner is about 1,300 lines, standard library only.
- **A DevTools port instead of a pipe.** Anything that can reach the port drives the browser; the pipe has no address.
- **`HeadlessExperimental.beginFrame`.** Frame-exact by construction, but it cannot capture a clip, and the measured captures were deterministic without it.
- **ProRes 4444 or VP9 with alpha** for the frames: section 3.
- **Rendering the preview at a device scale factor of 0.5.** Faster in theory; measured not deterministic (section 3).
- **Debian's chromium-headless-shell** instead of Chrome for Testing: the alternative if the licence review prefers a Debian-built binary; Chrome for Testing was chosen because it is the build Google tests the DevTools protocol against, published for both architectures at the same version.
- **Chrome's own sandbox inside the container** (user namespaces): would need capabilities or a seccomp profile the media sandbox does not grant; revisit if gVisor's user-namespace support makes it free.

## 12. Credits

The ideas of the virtual clock and of seeking paused animations frame by frame come from open-edit's page runtime (github.com/veedstudio/open-edit, Copyright 2026 VEED LIMITED, Apache-2.0); the motion vocabulary (word-by-word reveals with scale and blur, type as a character, new elements every 0.3 to 0.6 s, holds long enough to read) from herk-edit-kit's `MOTION_PHILOSOPHY.md` (github.com/nateherkai/herk-edit-kit, MIT). No code of either was copied or ported; `apps/server/internal/motion/THIRD_PARTY_NOTICES.md` records this.
