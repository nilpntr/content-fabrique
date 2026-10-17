package editor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// The text the tools hand to the model. Everything a person or a file wrote (titles, descriptions, the brief, the extra
// instructions, the spec of a timeline that a person may have edited) goes through runtime.Untrusted, and nothing else does.

// secs formats milliseconds as seconds with one decimal, "12.4 s".
func secs(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " s" }

// secs2 is secs with two decimals, for times on the timeline.
func secs2(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 2, 64) + " s" }

// cutRunes cuts s to at most n runes, adding an ellipsis when it cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// Orientation of a picture of w x h pixels.
func orientation(w, h int32) string {
	switch {
	case w <= 0 || h <= 0:
		return "unknown"
	case h > w:
		return "portrait"
	case w > h:
		return "landscape"
	}
	return "square"
}

// Caps on the creator's text in the run context, so that it stays far below the size of an event.
const (
	contextTitleChars       = 100
	contextDescriptionChars = 300
)

// footageText lists the clips. titleCap and descCap cut the creator's text (0: no cut).
func footageText(rows []store.ProjectFootageRow, titleCap, descCap int) string {
	if len(rows) == 0 {
		return "Footage: this project has no usable clips."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Footage: %d clips, in the creator's order. Only these can go on the timeline.\n", len(rows))
	for _, r := range rows {
		var dur int64
		if r.DurationMs != nil {
			dur = *r.DurationMs
		}
		var w, h int32
		if r.Width != nil {
			w = *r.Width
		}
		if r.Height != nil {
			h = *r.Height
		}
		size := ""
		if w > 0 && h > 0 {
			size = fmt.Sprintf(" %dx%d", w, h)
		}
		sound := "no"
		if r.HasAudio != nil && *r.HasAudio {
			sound = "yes"
		}
		fmt.Fprintf(&b, "\n- id %s | %d ms (%s) | %s%s | sound: %s | kind: %s\n", r.AssetID, dur, secs(dur), orientation(w, h), size, sound, r.Kind)
		title := r.Title
		if titleCap > 0 {
			title = cutRunes(title, titleCap)
		}
		fmt.Fprintf(&b, "  title: %s\n", runtime.Untrusted("clip_title", title))
		if d := strings.TrimSpace(r.Description); d != "" {
			if descCap > 0 {
				d = cutRunes(d, descCap)
			}
			fmt.Fprintf(&b, "  description: %s\n", runtime.Untrusted("clip_description", d))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// specStats are the numbers of a spec the model needs at a glance.
func specStats(spec *timeline.Spec) (total int64, clips, texts, sounds int) {
	total = timeline.TotalDuration(spec)
	for _, t := range spec.Tracks {
		switch t := t.(type) {
		case *timeline.VideoTrack:
			clips += len(t.Items)
		case *timeline.TextTrack:
			texts += len(t.Items)
		case *timeline.AudioTrack:
			sounds += len(t.Items)
		}
	}
	return total, clips, texts, sounds
}

// timelineLine is one line about a stored version.
func timelineLine(version int, spec *timeline.Spec) string {
	total, clips, texts, sounds := specStats(spec)
	return fmt.Sprintf("version %d: %d ms (%s) long, %d clips, %d texts, %d sounds", version, total, secs(total), clips, texts, sounds)
}

// timeMap lists the clips of the main track as they play: where each starts and ends on the timeline and which part of which
// asset it shows. At most limit lines.
func timeMap(spec *timeline.Spec, limit int) string {
	main, _, ok := timeline.MainTrack(spec)
	if !ok || len(main.Items) == 0 {
		return "(no clips)"
	}
	var b strings.Builder
	for i, it := range main.Items {
		if i >= limit {
			fmt.Fprintf(&b, "... and %d more clips\n", len(main.Items)-limit)
			break
		}
		fmt.Fprintf(&b, "%s: %s to %s, asset %s %d-%d ms", it.ID, secs2(it.AtMs), secs2(timeline.EndMs(it)), it.AssetID, it.Source.InMs, it.Source.OutMs)
		if sp := it.SpeedOrDefault(); sp != 1 {
			fmt.Fprintf(&b, " at speed %s", strconv.FormatFloat(sp, 'f', -1, 64))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// issuesText lists validation issues; severity first so errors stand out. At most limit lines.
func issuesText(issues []timeline.Issue, limit int) string {
	if len(issues) == 0 {
		return "none"
	}
	var b strings.Builder
	for i, is := range issues {
		if i >= limit {
			fmt.Fprintf(&b, "... and %d more\n", len(issues)-limit)
			break
		}
		fmt.Fprintf(&b, "- %s %s at %s: %s\n", is.Severity, is.Code, is.Path, is.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// opIssuesText lists the issues of an edit batch, each with the index of the op that caused it.
func opIssuesText(issues []ops.Issue, limit int) string {
	var b strings.Builder
	for i, is := range issues {
		if i >= limit {
			fmt.Fprintf(&b, "... and %d more\n", len(issues)-limit)
			break
		}
		where := "the batch"
		if is.OpIndex != ops.NoOp {
			where = fmt.Sprintf("op %d", is.OpIndex)
			if is.Op != "" {
				where += " (" + is.Op + ")"
			}
		}
		at := is.Field
		if at == "" {
			at = is.Path
		}
		if at != "" {
			at = " at " + at
		}
		fmt.Fprintf(&b, "- %s: %s %s%s: %s\n", where, is.Severity, is.Code, at, is.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// compactJSON removes the whitespace of a JSON document.
func compactJSON(raw []byte) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}
