package editor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/subject"
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

// footageOpts say how much of the creator's text footageText prints.
type footageOpts struct {
	// Full prints every title and description whole, for every clip (list_footage).
	Full bool
	// Otherwise the first Detailed clips get their title (cut to TitleChars runes, none if 0) and description (cut to DescChars,
	// none if 0); the others get their facts only.
	Detailed, TitleChars, DescChars int
	// Analysis holds the one-line summary of each analysed clip (reps, dead time, exposure). It is computed from the picture, not
	// written by a person, so it is printed as it is.
	Analysis map[ids.AssetID]analysis.Summary
	// Subjects is where the person is in each clip (the quick look's boxes): numbers only, printed as a line about where the head is.
	Subjects subject.Index
}

// footageText lists the clips.
func footageText(rows []store.ProjectFootageRow, o footageOpts) string {
	if len(rows) == 0 {
		return "Footage: this project has no usable clips."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Footage: %d clips, in the creator's order. Only these can go on the timeline.\n", len(rows))
	for n, r := range rows {
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
		if sum, ok := o.Analysis[r.AssetID]; ok {
			fmt.Fprintf(&b, "  analysis: %s\n", sum.Line())
		}
		if line := subjectLine(o.Subjects, r.AssetID.String()); line != "" {
			fmt.Fprintf(&b, "  subject: %s\n", line)
		}
		if !o.Full && n >= o.Detailed {
			continue
		}
		if title := r.Title; o.Full || o.TitleChars > 0 {
			if !o.Full {
				title = cutRunes(title, o.TitleChars)
			}
			fmt.Fprintf(&b, "  title: %s\n", runtime.Untrusted("clip_title", title))
		}
		if d := strings.TrimSpace(r.Description); d != "" && (o.Full || o.DescChars > 0) {
			if !o.Full {
				d = cutRunes(d, o.DescChars)
			}
			fmt.Fprintf(&b, "  description: %s\n", runtime.Untrusted("clip_description", d))
		}
	}
	if o.Analysis != nil {
		if n := unanalysed(rows, o.Analysis); n > 0 {
			fmt.Fprintf(&b, "\nThe analysis of %d video clip(s) is not ready yet, so find_moments knows nothing about them (it runs shortly after an upload).\n", n)
		}
	}
	if !o.Full && len(rows) > o.Detailed {
		b.WriteString("\nTitles and descriptions are shortened or left out here to keep this overview small; list_footage has them whole.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// subjectLine says where the person is in a clip, in numbers: the top of the head as a percentage of the frame height at each sampled
// time, and the clear band above the highest head ("subject top at about 22 % from the top"). "" for a clip with no subject box.
func subjectLine(ix subject.Index, assetID string) string {
	tr, ok := ix[assetID]
	if !ok || len(tr.Samples) == 0 {
		return ""
	}
	var parts []string
	for _, s := range tr.Samples {
		parts = append(parts, fmt.Sprintf("%.0f %% at %s", s.HeadTopY*100, secs(s.AtMs)))
	}
	top, _ := subject.TopPct(tr.Samples)
	return fmt.Sprintf("the top of the head is at about %.0f %% from the top of the frame (%s); a title above it needs the band between the top of the safe zone and that line. check_edit says when a title covers the head", top, strings.Join(parts, ", "))
}

// unanalysed counts the video clips that have no analysis yet.
func unanalysed(rows []store.ProjectFootageRow, got map[ids.AssetID]analysis.Summary) int {
	n := 0
	for _, r := range rows {
		if _, ok := got[r.AssetID]; !ok && r.Kind == store.AssetKindVideo {
			n++
		}
	}
	return n
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
		if it.HasRamp() {
			fmt.Fprintf(&b, " on a %d-point speed ramp", len(it.SpeedRamp))
		} else if sp := it.SpeedOrDefault(); sp != 1 {
			fmt.Fprintf(&b, " at speed %s", strconv.FormatFloat(sp, 'f', -1, 64))
		}
		if it.Motion.Active() {
			fmt.Fprintf(&b, ", motion %s", it.Motion.Preset)
		}
		if tr := it.TransitionIn; tr != nil && tr.Type != timeline.TransitionCut && tr.Type != "" {
			fmt.Fprintf(&b, ", %s in (%d ms)", tr.Type, tr.DurationMs)
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
