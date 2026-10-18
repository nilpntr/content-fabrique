package styleprofile

import (
	"fmt"
	"math"
	"strings"
)

// PlainLine is one measured fact about the creator's videos in plain words, for the app's "Details" (P6-05b): a label and a value,
// never a statistic's name.
type PlainLine struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// secs is a duration in seconds as a person says it: "about 2 s", "about 20 s".
func secs(v float64) string {
	switch {
	case v <= 0:
		return "0 s"
	case v < 10:
		return fmt.Sprintf("%.1f s", v)
	}
	return fmt.Sprintf("%.0f s", v)
}

func rangeSecs(r Range) string {
	if math.Round(r.Min) == math.Round(r.Max) {
		return "about " + secs(r.Median)
	}
	return fmt.Sprintf("%s to %s (usually about %s)", secs(r.Min), secs(r.Max), secs(r.Median))
}

// PlainLines are the measures of the profile in plain words: the shots, the opening, effects, the look, the title, the music and the
// length. No creator text (the title texts are not shown here).
func (p *Profile) PlainLines() []PlainLine {
	if p == nil {
		return nil
	}
	var out []PlainLine
	add := func(id, label, value string) { out = append(out, PlainLine{ID: id, Label: label, Value: value}) }

	add("references", "Learnt from", fmt.Sprintf("%d %s", p.References, plural(p.References, "video", "videos")))

	pc := p.Pacing
	cuts := fmt.Sprintf("%.0f to %.0f cuts a video", pc.CutsPerVideo.Min, pc.CutsPerVideo.Max)
	if pc.CutsPerVideo.Min == pc.CutsPerVideo.Max {
		cuts = fmt.Sprintf("%.0f %s a video", pc.CutsPerVideo.Min, plural(int(pc.CutsPerVideo.Min), "cut", "cuts"))
	}
	switch pc.Style {
	case PaceLongTake:
		add("shots", "Shots", fmt.Sprintf("Long takes that let the set run: %s, the longest shot about %s", cuts, secs(pc.LongestShotS.Median)))
	case PaceMontage:
		add("shots", "Shots", fmt.Sprintf("Quick cuts: %s, shots of about %s", cuts, secs(pc.ShotMedianS.Median)))
	default:
		add("shots", "Shots", fmt.Sprintf("A mix of long takes and quick cuts: %s", cuts))
	}
	if pc.ShotMedianS.Median > 0 {
		add("shot_length", "Shot length", fmt.Sprintf("Mostly about %s between cuts", secs(pc.ShotMedianS.Median)))
	}
	if p.Hook.FirstCutS.Median > 0 {
		add("first_cut", "Opening shot", fmt.Sprintf("The first cut comes about %s in", secs(p.Hook.FirstCutS.Median)))
	}
	switch p.Effects.Verdict {
	case EffectsNone:
		add("effects", "Effects", "None found: no zoom punches, flashes or speed changes")
	case EffectsLight:
		add("effects", "Effects", "A few: the odd zoom or flash")
	default:
		add("effects", "Effects", "Lots: zooms, flashes or speed changes in most videos")
	}
	look := "The camera's own picture, no colour filter"
	if p.Look.Grade == "graded" {
		look = "A colour treatment or vignette"
	}
	if p.Look.LowKey {
		look += ", in dark, moody light"
	}
	add("look", "Look", look)
	if t := p.Text; t != nil {
		if t.TitleShare == 0 {
			add("title", "Title", "No title on the videos")
		} else {
			var parts []string
			parts = append(parts, fmt.Sprintf("On %.0f %% of the videos", t.TitleShare*100))
			switch t.Case {
			case "upper":
				parts = append(parts, "all capitals")
			case "lower":
				parts = append(parts, "all lowercase")
			}
			if t.Colour != "" {
				parts = append(parts, "colour "+t.Colour)
			}
			switch t.Position {
			case "upper_third":
				parts = append(parts, "at the top")
			case "center":
				parts = append(parts, "in the middle")
			case "lower_third":
				parts = append(parts, "low on the screen")
			}
			if t.OneLineShare >= 0.5 {
				parts = append(parts, "one line")
			}
			if t.LabelShare == 0 {
				parts = append(parts, "no other text")
			}
			add("title", "Title", strings.Join(parts, ", "))
		}
	}
	a := p.Audio
	var sound []string
	switch {
	case a.MusicShare >= 0.99:
		sound = append(sound, "Music on every video")
	case a.MusicShare >= 0.6:
		sound = append(sound, "Music on most videos")
	case a.MusicShare > 0:
		sound = append(sound, "Music on some videos")
	default:
		sound = append(sound, "No music found")
	}
	if a.LiftShare >= 0.6 && a.IntroS.Median > 0 {
		sound = append(sound, fmt.Sprintf("quiet for about %s, then it lifts", secs(a.IntroS.Median)))
	}
	switch a.Arc {
	case ArcBuilds:
		sound = append(sound, "builds to the end")
	case ArcFalls:
		sound = append(sound, "calms down toward the end")
	}
	if a.BeatLocked {
		sound = append(sound, "cuts on the beat")
	} else if a.CutsMeasured >= MinCutsForBeat {
		sound = append(sound, "cuts near the beat, not on it")
	}
	add("music", "Music", strings.Join(sound, ", "))
	if p.Format.DurationS.Max > 0 {
		add("length", "Length", rangeSecs(p.Format.DurationS))
	}
	// What P6-05c learnt.
	if pc.EndShotS.Median > 0 {
		if pc.LongEndingHold() {
			add("ending", "Ending", fmt.Sprintf("A long hold on the last shot, about %s", secs(pc.EndShotS.Median)))
		} else {
			add("ending", "Ending", fmt.Sprintf("The last shot is about as long as the others (%s)", secs(pc.EndShotS.Median)))
		}
	}
	if pc.ShotS.P90 > 0 {
		add("shot_tail", "Longest shots", fmt.Sprintf("Most shots under %s", secs(pc.ShotS.P90)))
	}
	switch tp := p.Transitions; {
	case tp.Measured == 0:
	case tp.Used == TransitionsCutOnly:
		add("transitions", "Transitions", "Plain cuts only")
	case tp.Used == TransitionsSome:
		var kinds []string
		for _, k := range tp.Kinds {
			kinds = append(kinds, map[string]string{"blend": "blends", "flash": "flashes", "zoom": "zooms"}[k])
		}
		add("transitions", "Transitions", fmt.Sprintf("Some: %s, on %.0f %% of the cuts", strings.Join(kinds, " and "), tp.Share*100))
	}
	switch p.Camera.Level {
	case CameraStatic:
		add("camera", "Camera", "Steady, on a tripod")
	case CameraSubtle:
		add("camera", "Camera", "A little movement")
	case CameraHandheld:
		add("camera", "Camera", "Handheld, it follows you")
	}
	if m := p.Movement; m.Known() {
		v := "Cuts land at the end of a rep"
		if m.MidRepShare >= 0.5 {
			v = "Cuts mid-rep"
		}
		if m.InsideSetShare >= 0.25 {
			v += "; switches angles within an exercise"
		}
		add("cuts", "Cuts and reps", v)
	}
	if t := p.Text; t != nil {
		if t.Entrance != "" {
			add("title_entrance", "Title entrance", map[string]string{"none": "The title is just there", "pop": "The title pops in", "slide": "The title slides in", "fade": "The title fades in", "typewriter": "The title types itself"}[t.Entrance])
		}
		if len(t.Templates) > 0 && t.Templates[0].Share >= PatternMinShare {
			add("title_form", "Title form", slotWords(t.Templates[0].Template))
		}
		if t.ChapterRead > 0 {
			if t.NumberedChapters() {
				add("chapters", "Chapters", "Numbered chapter titles in videos with several exercises")
			} else {
				add("chapters", "Chapters", "No chapter titles")
			}
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
