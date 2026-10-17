package render

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// ErrInvalidSpec is wrapped by every error Compile returns for a spec it cannot render. Compile does not validate:
// it expects a spec that timeline.Validate accepted, and only refuses what it cannot build a graph from.
var ErrInvalidSpec = errors.New("render: spec cannot be compiled")

// ErrGraphTooLong is the cause of an ErrInvalidSpec whose filter graph is over MaxFilterGraphBytes.
var ErrGraphTooLong = errors.New("the filter graph is too long")

// MaxFilterGraphBytes is the longest filter graph Compile writes. The graph is one argument of the ffmpeg process, and Linux limits
// a single argument to 128 KiB (MAX_ARG_STRLEN): a longer graph would fail in the sandbox with "argument list too long" and
// nothing to say why. A sound effect is 0.7 to 6 kB of it (about 3 kB on average), so the limit is some 40 effects on top of a
// normal edit.
const MaxFilterGraphBytes = 120_000

// clip is a main-track item with everything the compiler derives from its position.
type clip struct {
	idx   int
	item  timeline.VideoItem
	asset ResolvedAsset
	// startFrame and endFrame bound the clip's own footage, both snapped from the cumulative at_ms: [startFrame,
	// endFrame). ownedEnd is where the next clip starts (the last clip: endFrame), so [startFrame, ownedEnd) is what
	// the time map assigns to the clip.
	startFrame, endFrame, ownedEnd int64
	// overlapFrames is how many frames the clip overlaps the previous one (a transition), 0 for a cut.
	overlapFrames int64
	transition    string
	// outFrames is the number of frames the clip's graph produces.
	outFrames int64
}

type compiler struct {
	spec    *timeline.Spec
	profile Profile
	w, h    int
	fps     int
	clips   []clip
	total   int64
	totalMs int64
	// inputs are the ffmpeg inputs: one per clip, then one per audio track item.
	inputs []Input
	plan   Plan
}

// Compile builds the Plan for rendering spec with profile. It is pure and deterministic: no process is started and
// nothing is read apart from what resolver returns.
func Compile(spec *timeline.Spec, profile Profile, resolver AssetResolver) (Plan, error) {
	if spec == nil {
		return Plan{}, fmt.Errorf("%w: nil spec", ErrInvalidSpec)
	}
	if resolver == nil {
		return Plan{}, errors.New("render: nil asset resolver")
	}
	if profile.SizeDivisor < 1 {
		return Plan{}, errors.New("render: profile has no size divisor")
	}
	c := &compiler{spec: spec, profile: profile, fps: spec.Output.FPS}
	if c.fps <= 0 {
		return Plan{}, fmt.Errorf("%w: fps %d", ErrInvalidSpec, c.fps)
	}
	// Half of 1080x1350 (4:5) is 540x675, and yuv420p needs even dimensions: the profile size rounds up to even (676).
	c.w = scaledDim(spec.Output.Width, profile.SizeDivisor)
	c.h = scaledDim(spec.Output.Height, profile.SizeDivisor)
	if c.w < 2 || c.h < 2 {
		return Plan{}, fmt.Errorf("%w: output size %dx%d", ErrInvalidSpec, spec.Output.Width, spec.Output.Height)
	}
	if err := c.layout(resolver); err != nil {
		return Plan{}, err
	}

	// Text, captions and the preview watermark are one generated ASS script, burned in after the clips are joined.
	subs, err := c.subtitles()
	if err != nil {
		return Plan{}, err
	}

	// The video graph uses inputs 0..n-1 (one per clip); the audio graph appends the audio track items.
	video, err := c.videoGraph(subs)
	if err != nil {
		return Plan{}, err
	}
	audio, err := c.audioGraph(resolver)
	if err != nil {
		return Plan{}, err
	}

	c.plan.Profile = profile
	c.plan.OutputSize = Size{Width: c.w, Height: c.h}
	c.plan.FPS = c.fps
	c.plan.TotalFrames = c.total
	c.plan.DurationMs = c.totalMs
	c.plan.Inputs = c.inputs
	c.plan.Subtitles = subs
	c.plan.FilterComplex = strings.Join(append(video, audio.statements...), ";")
	if n := len(c.plan.FilterComplex); n > MaxFilterGraphBytes {
		return Plan{}, fmt.Errorf("%w: %w: %d bytes, over the %d bytes that one argument of a process can hold: use fewer sound effects or shorter edits", ErrInvalidSpec, ErrGraphTooLong, n, MaxFilterGraphBytes)
	}
	c.plan.Loudnorm = audio.loudnorm
	c.plan.Args = c.args()
	if audio.loudnorm != nil {
		audio.loudnorm.Pass1Args = c.pass1Args(audio.pass1Graph)
	}
	c.collectMaps()
	return c.plan, nil
}

// layout sorts the main track, snaps every boundary to the frame grid and resolves the clips' assets.
func (c *compiler) layout(resolver AssetResolver) error {
	main, _, ok := timeline.MainTrack(c.spec)
	if !ok || len(main.Items) == 0 {
		return fmt.Errorf("%w: no clips on the main track", ErrInvalidSpec)
	}
	items := slices.Clone(main.Items)
	slices.SortStableFunc(items, func(a, b timeline.VideoItem) int {
		switch {
		case a.AtMs < b.AtMs:
			return -1
		case a.AtMs > b.AtMs:
			return 1
		}
		return 0
	})

	resolved := map[string]ResolvedAsset{}
	for i, it := range items {
		if !timeline.Playable(it) {
			return fmt.Errorf("%w: clip %s plays nothing (in_ms %d, out_ms %d)", ErrInvalidSpec, it.ID, it.Source.InMs, it.Source.OutMs)
		}
		ra, seen := resolved[it.AssetID]
		if !seen {
			var err error
			ra, err = resolver.Resolve(it.AssetID, c.profile.Source)
			if err != nil {
				return fmt.Errorf("render: resolve asset %s for clip %s: %w", it.AssetID, it.ID, err)
			}
			resolved[it.AssetID] = ra
		}
		cl := clip{
			idx:        i,
			item:       it,
			asset:      ra,
			startFrame: timeline.Frame(it.AtMs, c.fps),
			endFrame:   timeline.Frame(timeline.EndMs(it), c.fps),
			transition: timeline.TransitionCut,
		}
		cl.outFrames = cl.endFrame - cl.startFrame
		if cl.outFrames < 1 {
			return fmt.Errorf("%w: clip %s is shorter than one frame at %d fps", ErrInvalidSpec, it.ID, c.fps)
		}
		if it.TransitionIn != nil && it.TransitionIn.Type != "" {
			cl.transition = it.TransitionIn.Type
		}
		c.clips = append(c.clips, cl)
	}

	for i := range c.clips {
		cl := &c.clips[i]
		if i+1 < len(c.clips) {
			cl.ownedEnd = c.clips[i+1].startFrame
		} else {
			cl.ownedEnd = cl.endFrame
		}
		if i == 0 {
			if cl.startFrame != 0 {
				return fmt.Errorf("%w: the first clip %s starts at frame %d, not 0", ErrInvalidSpec, cl.item.ID, cl.startFrame)
			}
			cl.transition = timeline.TransitionCut
			continue
		}
		prev := c.clips[i-1]
		overlap := prev.endFrame - cl.startFrame
		switch {
		case overlap < 0:
			return fmt.Errorf("%w: gap of %d frames before clip %s", ErrInvalidSpec, -overlap, cl.item.ID)
		case cl.transition == timeline.TransitionCut && overlap != 0:
			return fmt.Errorf("%w: clip %s overlaps clip %s by %d frames without a transition", ErrInvalidSpec, cl.item.ID, prev.item.ID, overlap)
		case cl.transition != timeline.TransitionCut && overlap == 0:
			return fmt.Errorf("%w: clip %s has a %s transition but does not overlap clip %s at %d fps", ErrInvalidSpec, cl.item.ID, cl.transition, prev.item.ID, c.fps)
		case overlap >= min(prev.outFrames, cl.outFrames):
			return fmt.Errorf("%w: the transition into clip %s overlaps %d frames, which is not fewer than the frames of the clips it joins (%s plays %d, %s plays %d)",
				ErrInvalidSpec, cl.item.ID, overlap, prev.item.ID, prev.outFrames, cl.item.ID, cl.outFrames)
		}
		cl.overlapFrames = overlap
		if cl.transition != timeline.TransitionCut && xfadeName(cl.transition) == "" {
			return fmt.Errorf("%w: clip %s has unknown transition %q", ErrInvalidSpec, cl.item.ID, cl.transition)
		}
	}

	// The transitions into and out of a clip must leave at least one frame of the clip on its own.
	for i, cl := range c.clips {
		in := cl.overlapFrames
		var out int64
		if i+1 < len(c.clips) {
			out = c.clips[i+1].overlapFrames
		}
		if in+out >= cl.outFrames {
			return fmt.Errorf("%w: clip %s plays %d frames, but the transitions into and out of it take %d and %d: together they must be fewer than the clip's frames", ErrInvalidSpec, cl.item.ID, cl.outFrames, in, out)
		}
	}

	// The timeline ends where the latest clip ends (the same definition Validate uses).
	c.total = timeline.Frame(timeline.PlayableEnd(items), c.fps)
	c.totalMs = timeline.FrameStartMs(c.total, c.fps)

	// One input per clip, in track order.
	for _, cl := range c.clips {
		c.inputs = append(c.inputs, Input{
			Index:      cl.idx,
			Role:       RoleClip,
			ItemID:     cl.item.ID,
			AssetID:    cl.item.AssetID,
			Location:   cl.asset.Location,
			SeekMs:     cl.item.Source.InMs,
			DurationMs: cl.item.Source.OutMs - cl.item.Source.InMs,
		})
	}
	return nil
}

// args builds the ffmpeg argument list up to the output path.
func (c *compiler) args() []string {
	args := c.inputArgs(nil)
	args = append(args, "-filter_complex", c.plan.FilterComplex, "-map", "[vout]", "-map", "[aout]")
	p := c.profile
	args = append(args, "-c:v", "libx264", "-preset", p.X264Preset, "-crf", strconv.Itoa(p.CRF))
	if p.X264Profile != "" {
		args = append(args, "-profile:v", p.X264Profile)
	}
	args = append(args, "-pix_fmt", "yuv420p")
	args = append(args, "-c:a", "aac", "-b:a", p.AudioBitrate, "-ar", strconv.Itoa(AudioSampleRate), "-ac", "2")
	// Deterministic output: no metadata from the sources, and no encoder version or timestamps in the file.
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact")
	if p.Faststart {
		args = append(args, "-movflags", "+faststart")
	}
	return args
}

// pass1Args is the loudness measuring pass: the audio graph only, to a null muxer, with only the inputs whose audio it
// uses. The graph (built with the clips' and items' own input numbers) is renumbered to match.
func (c *compiler) pass1Args(graph string) []string {
	return c.audioOnlyArgs(graph, []string{"[aout]"})
}

// audioOnlyArgs is an ffmpeg command over the audio part of the graph that opens only the inputs the graph uses (renumbering their
// labels): the loudness pass and the bed's evening pass. It maps the given pads and stops before the output.
func (c *compiler) audioOnlyArgs(graph string, maps []string) []string {
	used := map[int]int{}
	for _, st := range splitStatements(graph) {
		if m := audioInputRe.FindStringSubmatch(st); m != nil {
			k, _ := strconv.Atoi(m[1])
			if _, ok := used[k]; !ok {
				used[k] = -1
			}
		}
	}
	keep := make([]int, 0, len(used))
	for k := range used {
		keep = append(keep, k)
	}
	slices.Sort(keep)
	for rank, k := range keep {
		used[k] = rank
	}
	stmts := splitStatements(graph)
	for i, st := range stmts {
		if m := audioInputRe.FindStringSubmatch(st); m != nil {
			k, _ := strconv.Atoi(m[1])
			stmts[i] = fmt.Sprintf("[%d:a]", used[k]) + st[len(m[0]):]
		}
	}
	args := c.inputArgs(keep)
	args = append(args, "-filter_complex", strings.Join(stmts, ";"))
	for _, m := range maps {
		args = append(args, "-map", m)
	}
	return append(args, "-vn")
}

// audioInputRe matches the input label that starts a statement of the audio graph.
var audioInputRe = regexp.MustCompile(`^\[(\d+):a\]`)

// inputArgs returns the input arguments of the inputs with the given indexes, or of all of them when only is nil. Every
// input is opened as an untrusted file: local files only (the sandbox has no network and the worker stages the assets),
// and only the container formats uploads may have, so an uploaded playlist cannot make ffmpeg read other files.
func (c *compiler) inputArgs(only []int) []string {
	var args []string
	for _, in := range c.inputs {
		if only != nil && !slices.Contains(only, in.Index) {
			continue
		}
		args = append(args, "-protocol_whitelist", "file", "-format_whitelist", media.UntrustedFormats)
		if in.SeekMs > 0 {
			args = append(args, "-ss", secs(in.SeekMs))
		}
		args = append(args, "-t", secs(in.DurationMs), "-i", in.Location)
	}
	return args
}

// collectMaps fills the cut list and the time map of the plan.
func (c *compiler) collectMaps() {
	p := &c.plan
	p.CutList = []Cut{}
	p.TimeMap = []TimeMapEntry{}
	p.SoundCues = append([]SoundCue{}, p.SoundCues...)

	for i, cl := range c.clips {
		if i > 0 {
			p.CutList = append(p.CutList, Cut{
				Index:            i,
				ItemID:           cl.item.ID,
				Frame:            cl.startFrame,
				AtMs:             timeline.FrameStartMs(cl.startFrame, c.fps),
				Transition:       cl.transition,
				TransitionFrames: cl.overlapFrames,
			})
		}
		in := cl.item.Source.InMs
		// A clip with a speed ramp reports its average speed as `speed` (for readers that know nothing of ramps) and the ramp
		// itself, which maps output time to source time exactly (TimeMapEntry.SourceAtMs).
		speed := cl.item.AverageSpeed()
		p.TimeMap = append(p.TimeMap, TimeMapEntry{
			OutStartMs: timeline.FrameStartMs(cl.startFrame, c.fps),
			OutEndMs:   timeline.FrameStartMs(cl.ownedEnd, c.fps),
			ItemID:     cl.item.ID,
			AssetID:    cl.item.AssetID,
			SourceInMs: &in,
			Speed:      &speed,
			SpeedRamp:  slices.Clone(cl.item.SpeedRamp),
		})
	}

	for _, t := range c.spec.Tracks {
		tr, ok := t.(*timeline.TextTrack)
		if !ok {
			continue
		}
		for _, it := range tr.Items {
			start := timeline.Frame(it.AtMs, c.fps)
			end := timeline.Frame(it.AtMs+it.DurationMs, c.fps)
			p.TimeMap = append(p.TimeMap, TimeMapEntry{
				OutStartMs: timeline.FrameStartMs(start, c.fps),
				OutEndMs:   timeline.FrameStartMs(end, c.fps),
				ItemID:     it.ID,
			})
		}
	}
}

// secs formats milliseconds as seconds with three decimals, the form ffmpeg reads exactly.
func secs(ms int64) string {
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	return fmt.Sprintf("%s%d.%03d", sign, ms/1000, ms%1000)
}

// frameSecs formats a frame count as seconds with microsecond precision, rounded half up the way ffmpeg rounds a
// timestamp to its microsecond time base, so an xfade offset lands on the frame it was computed from.
func (c *compiler) frameSecs(frames int64) string {
	us := (2*frames*1_000_000 + int64(c.fps)) / (2 * int64(c.fps))
	return fmt.Sprintf("%d.%06d", us/1_000_000, us%1_000_000)
}

// num formats a float for a filter argument: at most six decimals, no trailing zeros.
func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func round6(f float64) float64 {
	return float64(int64(f*1e6+0.5)) / 1e6
}

// scaledDim divides a dimension by the profile's divisor and rounds the result up to an even number.
func scaledDim(n, divisor int) int {
	return (n/divisor + 1) &^ 1
}

// even rounds a size down to an even number of at least 2.
func even(n int) int {
	return max(2, n/2*2)
}

// chain formats one statement of the graph: [in...]f1,f2,...[out].
func chain(in []string, filters []string, out string) string {
	var b strings.Builder
	for _, l := range in {
		b.WriteString("[" + l + "]")
	}
	b.WriteString(strings.Join(filters, ","))
	b.WriteString("[" + out + "]")
	return b.String()
}
