package styleprofile

import (
	"fmt"
	"strings"
)

// structureLines are the measures of P6-05c as lines: the shot-length tail and the ending hold, the transitions, the camera, where the
// cuts sit against the movement, the title's entrance, forms and the chapter titles. withWords puts the title forms in (they hold the
// creator's own fixed words: only for the untrusted creator_style block and cfctl); without, they are only said to exist.
func (p *Profile) structureLines(withWords bool) []string {
	if p == nil {
		return nil
	}
	var out []string
	pc := p.Pacing
	if pc.ShotS.Median > 0 {
		line := fmt.Sprintf("shot lengths (every shot of the videos with a cut): %s s", pc.ShotS)
		if p90 := pc.P90(); p90 > 0 {
			line += fmt.Sprintf(", 90 %% of the shots at most %.1f s", p90)
		}
		if pc.EndingS.Median > 0 {
			line += fmt.Sprintf("; the last shot is held %s s", pc.EndingS)
			if pc.LongEndingHold() {
				line += " (a long hold at the end)"
			}
		}
		out = append(out, line)
	}
	if tp := p.Transitions; tp.Measured > 0 {
		switch {
		case tp.Used == TransitionsNone:
			out = append(out, "transitions measured in the picture: none (the videos have no cuts)")
		case tp.Used == TransitionsCutOnly:
			out = append(out, "transitions measured in the picture: plain cuts only")
		case !tp.Evident():
			out = append(out, fmt.Sprintf("transitions measured in the picture: plain cuts (%s on %.0f %% of the cuts in %d of the videos, too few to be the style)", strings.Join(tp.Kinds, ", "), tp.Share*100, tp.Videos))
		default:
			out = append(out, fmt.Sprintf("transitions measured in the picture: %s on %.0f %% of the cuts, in %d of the videos", strings.Join(tp.Kinds, ", "), tp.Share*100, tp.Videos))
		}
	}
	if p.Camera.Level == CameraSubtle {
		out = append(out, "camera: a little movement (not hand-held, not static)")
	}
	if m := p.Movement; m.Known() {
		out = append(out, fmt.Sprintf("cuts against the movement (%d cuts measured): %.0f %% at the end of a rep or after a set, %.0f %% in the middle of a rep; %.0f %% with reps at the same pace on both sides (most likely an angle switch within one exercise)",
			m.CutsMeasured, m.OnBoundaryShare*100, m.MidRepShare*100, m.InsideSetShare*100))
	} else if pc.CutsPerVideo.Max > 0 {
		out = append(out, "cuts against the movement: not measurable (the shots around the cuts are too short to find reps in)")
	}
	if t := p.Text; t != nil {
		if t.Entrance != "" {
			out = append(out, "title entrance: "+t.Entrance)
		}
		if len(t.Templates) > 0 {
			if withWords {
				var forms []string
				for _, tt := range t.Templates[:min(len(t.Templates), 3)] {
					f := fmt.Sprintf("'%s' (%.0f %% of the titles", tt.Template, tt.Share*100)
					if tt.Chars > 0 {
						f += fmt.Sprintf(", about %d characters", tt.Chars)
					}
					if tt.Lines > 0 {
						f += fmt.Sprintf(", %d %s", tt.Lines, map[bool]string{true: "line", false: "lines"}[tt.Lines == 1])
					}
					forms = append(forms, f+")")
				}
				out = append(out, "title forms (fill the slots from the footage; never copy a title of theirs): "+strings.Join(forms, "; "))
			} else if t.Templates[0].Share >= PatternMinShare {
				out = append(out, "titles follow a fixed form")
			}
		}
		if t.ChapterRead > 0 {
			line := fmt.Sprintf("numbered chapter titles in %.0f %% of the videos with several shots", t.ChapterShare*100)
			if withWords && len(t.ChapterTemplates) > 0 {
				var forms []string
				for _, tt := range t.ChapterTemplates[:min(len(t.ChapterTemplates), 2)] {
					forms = append(forms, "'"+tt.Template+"'")
				}
				line += " (forms: " + strings.Join(forms, ", ") + ")"
			}
			out = append(out, line)
		}
	}
	return out
}

// slotWords writes a template for the creator: the slots in small words ("<topic> <number>").
func slotWords(template string) string {
	r := strings.NewReplacer(SlotNumber, "<number>", SlotTopic, "<topic>", SlotGoal, "<goal>", SlotMore, "<more>")
	return r.Replace(template)
}
