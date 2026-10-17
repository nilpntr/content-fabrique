package render

import (
	"encoding/json"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Plan is everything Compile knows about one render. It is plain data: it can be stored next to the render, diffed
// in a golden test and handed to Execute later, possibly in another process.
type Plan struct {
	Profile Profile `json:"profile"`
	// OutputSize is the size of the rendered video: the spec's size divided by the profile's SizeDivisor.
	OutputSize Size `json:"output_size"`
	FPS        int  `json:"fps"`
	// TotalFrames and DurationMs are the length of the output. DurationMs is the start of frame TotalFrames.
	TotalFrames int64 `json:"total_frames"`
	DurationMs  int64 `json:"duration_ms"`

	// Inputs are the ffmpeg inputs, in -i order. Input k is [k:v] and [k:a] in FilterComplex.
	Inputs []Input `json:"inputs"`
	// FilterComplex is the whole graph. It is also inside Args, after -filter_complex.
	FilterComplex string `json:"filter_complex"`
	// Args is the ffmpeg argument list up to, and not including, the output path (and without the program name and
	// the logging and progress flags, which Execute adds). Use Plan.OutputArgs to complete it.
	Args []string `json:"args"`

	// CutList holds one entry per join on the main track, snapped to frames from the cumulative at_ms.
	CutList []Cut `json:"cut_list"`
	// TimeMap maps output time back to source time (design note section 6, outputs).
	TimeMap []TimeMapEntry `json:"time_map"`
	// SoundCues are the cue_only sounds. They are never mixed in, in any profile (D-03); the export package hands them
	// to the creator as "add this sound in the app".
	SoundCues []SoundCue `json:"sound_cues"`

	// Loudnorm is nil when the mix has no signal to normalise (a silent timeline).
	Loudnorm *LoudnormPlan `json:"loudnorm,omitempty"`

	// Subtitles is the generated ASS script for the text items, the captions and the preview watermark (P3-04), burned in
	// by the ass filter that ends the video graph; nil when there is nothing to draw. Execute writes the script and the
	// fonts it names into the work directory before ffmpeg runs.
	Subtitles *Subtitles `json:"subtitles,omitempty"`
	// Warnings lists what was left out because it could not be drawn safely: a text item or a caption whose box did not fit the
	// safe zone (text_dropped, caption_dropped).
	Warnings []Warning `json:"warnings,omitempty"`
}

// Size is a width and height in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Input is one ffmpeg input: a window of an asset.
type Input struct {
	Index   int    `json:"index"`
	Role    string `json:"role"` // RoleClip or RoleAudio
	ItemID  string `json:"item_id"`
	AssetID string `json:"asset_id"`
	// Location is what the resolver returned: an absolute local path.
	Location string `json:"location"`
	// SeekMs and DurationMs are the -ss and -t of the input: the window of the asset the item plays.
	SeekMs     int64 `json:"seek_ms"`
	DurationMs int64 `json:"duration_ms"`
}

// The Input roles.
const (
	RoleClip  = "clip"
	RoleAudio = "audio"
)

// Cut is a join between two items of the main track.
type Cut struct {
	// Index is the position of the incoming item on the main track (the first item has no cut).
	Index  int    `json:"index"`
	ItemID string `json:"item_id"`
	// Frame is the first output frame of the incoming item and AtMs the time that frame starts at.
	Frame int64 `json:"frame"`
	AtMs  int64 `json:"at_ms"`
	// Transition is the spec's transition type (cut, crossfade, dip_to_black, whip, whip_left, whip_right, zoom_through or flash). TransitionFrames is how many frames both items overlap
	// (0 for a cut), which is the spec's duration_ms snapped to the frame grid.
	Transition       string `json:"transition"`
	TransitionFrames int64  `json:"transition_frames"`
}

// TimeMapEntry is one entry of the time map. Main items carry the asset, the source in point and the speed; text
// items carry the first three fields only. A main item with a speed ramp (P3-10) also carries the ramp, and then `speed`
// is its average speed: source_in_ms + (t - out_start_ms) * speed is only an approximation inside such an item, and
// SourceAtMs is exact.
type TimeMapEntry struct {
	OutStartMs int64                 `json:"out_start_ms"`
	OutEndMs   int64                 `json:"out_end_ms"`
	ItemID     string                `json:"item_id"`
	AssetID    string                `json:"asset_id,omitempty"`
	SourceInMs *int64                `json:"source_in_ms,omitempty"`
	Speed      *float64              `json:"speed,omitempty"`
	SpeedRamp  []timeline.SpeedPoint `json:"speed_ramp,omitempty"`
}

// SourceAtMs is the time inside the item's asset that is on screen at output time outMs (milliseconds on the timeline):
// source_in_ms + (outMs - out_start_ms) * speed, or along the speed ramp. The result is a float, so a caller can round it
// the way it needs to; it is meaningless for a text item (SourceInMs is nil), for which it returns false.
func (e TimeMapEntry) SourceAtMs(outMs float64) (float64, bool) {
	if e.SourceInMs == nil || e.Speed == nil {
		return 0, false
	}
	d := outMs - float64(e.OutStartMs)
	if len(e.SpeedRamp) > 0 {
		return float64(*e.SourceInMs) + timeline.RampSourceMs(e.SpeedRamp, d), true
	}
	return float64(*e.SourceInMs) + d**e.Speed, true
}

// OutAtMs is the inverse of SourceAtMs: the output time at which source time sourceMs of the item's asset is on screen.
func (e TimeMapEntry) OutAtMs(sourceMs float64) (float64, bool) {
	if e.SourceInMs == nil || e.Speed == nil || *e.Speed <= 0 {
		return 0, false
	}
	d := sourceMs - float64(*e.SourceInMs)
	if len(e.SpeedRamp) > 0 {
		return float64(e.OutStartMs) + timeline.RampOutMs(e.SpeedRamp, d), true
	}
	return float64(e.OutStartMs) + d / *e.Speed, true
}

// SoundCue is a cue_only sound (D-03). Its audio is not available to the renderer and never appears in the plan's
// inputs or arguments.
type SoundCue struct {
	ItemID         string `json:"item_id"`
	TrendID        string `json:"trend_id"`
	AtMs           int64  `json:"at_ms"`
	SourceOffsetMs int64  `json:"source_offset_ms"`
	// DurationMs is the effective duration: the item's duration_ms, or everything up to the end of the timeline.
	DurationMs int64   `json:"duration_ms"`
	GainDB     float64 `json:"gain_db"`
}

// LoudnormPlan describes the loudness normalisation of the mix.
type LoudnormPlan struct {
	// Filter is the single-pass loudnorm filter exactly as it appears in FilterComplex and Args.
	Filter string `json:"filter"`
	// TwoPass is true for the final profile: Execute runs Pass1Args first and swaps Filter for the measured one.
	TwoPass bool `json:"two_pass"`
	// Pass1Args measures the mix: the audio part of the graph ending in loudnorm with print_format=json. Like Args it
	// stops before the output (Execute adds "-f null -").
	Pass1Args []string `json:"pass1_args,omitempty"`
}

// OutputArgs returns Args followed by -y and the output path, so an existing file is replaced.
func (p Plan) OutputArgs(output string) []string {
	args := make([]string, 0, len(p.Args)+2)
	args = append(args, p.Args...)
	return append(args, "-y", output)
}

// TimeMapJSON is the time map as the JSON array the render job stores.
func (p Plan) TimeMapJSON() ([]byte, error) {
	m := p.TimeMap
	if m == nil {
		m = []TimeMapEntry{}
	}
	return json.Marshal(m)
}

// SourceTimeMs resolves an output time to the main item that owns it and the source time inside that item's asset:
// source_in_ms + (t - out_start_ms) * speed. It returns false for a time outside the output.
func (p Plan) SourceTimeMs(outMs int64) (entry TimeMapEntry, sourceMs int64, ok bool) {
	for _, e := range p.TimeMap {
		if e.SourceInMs == nil || e.Speed == nil {
			continue
		}
		if outMs >= e.OutStartMs && outMs < e.OutEndMs {
			src, _ := e.SourceAtMs(float64(outMs))
			return e, int64(src + 0.5), true
		}
	}
	return TimeMapEntry{}, 0, false
}

// PrettyFilter puts one filter_complex statement on each line, for golden files and logs. The graph itself is never written
// with newlines. A semicolon inside single quotes is not a statement end: the sound effects' asendcmd commands are quoted lists
// separated by semicolons.
func PrettyFilter(filterComplex string) string {
	return strings.Join(splitStatements(filterComplex), "\n")
}

// splitStatements splits a filter_complex at the semicolons that are not inside single quotes; every piece but the last keeps
// its semicolon.
func splitStatements(g string) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(g); i++ {
		switch g[i] {
		case '\'':
			quoted = !quoted
		case ';':
			if !quoted {
				out = append(out, g[start:i+1])
				start = i + 1
			}
		}
	}
	return append(out, g[start:])
}
