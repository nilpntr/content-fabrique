package editor

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/hooklab"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// CheckHookScore is the opening of the edit against the hook lab's rubric (docs/design/hook-lab.md): the hook lands in the first two
// seconds, the text is short enough to read and off the person's face and body, the first frame is live picture, and a spoken line
// starts on the first word of a sentence that is not a filler. It is one number from the same rubric plan_edit's hook lab ranks the
// proposed openings with, so what the plan picked and what the edit built are measured alike. hook_length, hook_title, hook_line and
// text_over_subject each judge one of those things on their own; this check fails only when the opening as a whole scores under
// hooklab.PassTotal. It is in the report only for an edit whose plan ran the hook lab (Plan.HookLab), so the report of an older
// version, and with it the scores of the eval suites' stored versions, is what it was.
const CheckHookScore = "hook_score"

// SharpSpan is how sharp a stretch of a clip is, against the sharpest of the clip (the sharpness_rel of a moment of the clip analysis).
type SharpSpan struct {
	StartMs, EndMs int64
	Rel            float64
}

// sharpnessAt is the sharpness score (0 to 1) of the clip at t: that of the shortest stretch that holds t (a rep says more about its
// frames than the whole shot does). ok is false when no stretch holds it.
func sharpnessAt(spans []SharpSpan, t int64) (score float64, ok bool) {
	best := -1
	for i, s := range spans {
		if t < s.StartMs || t >= s.EndMs {
			continue
		}
		if best < 0 || s.EndMs-s.StartMs < spans[best].EndMs-spans[best].StartMs {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return analysis.SharpnessScore(spans[best].Rel), true
}

// hookPicture is what the analysis knows about a clip as a first shot from inMs: its dead time, how lit its first frames are and how
// sharp. nil for a clip that was not analysed.
func hookPicture(a AssetAnalysis, sharp []SharpSpan, inMs int64) *hooklab.Picture {
	if !a.Analysed {
		return nil
	}
	p := &hooklab.Picture{}
	for _, d := range a.Dead {
		p.Dead = append(p.Dead, hooklab.Span{StartMs: d.StartMs, EndMs: d.EndMs})
	}
	if a.HasSeries {
		if ex, ok := a.Series.ExposureBetween(int(inMs), int(inMs+hooklab.FirstFrameMs)); ok {
			p.Lit = &ex.P90
		}
	}
	if s, ok := sharpnessAt(sharp, inMs); ok {
		p.Sharpness = &s
	}
	return p
}

// hookSpeech is a clip's transcript as the hook lab reads it.
func hookSpeech(sp *Speech) *hooklab.Speech {
	if sp == nil {
		return nil
	}
	return &hooklab.Speech{Words: sp.Words, Structure: sp.Structure}
}

// textPlace says where a text ends up against the person in the clips under it (one of hooklab's Text constants): where the renderer
// shows it (subject.FitText, the function the renderer and text_over_subject use), or what it covers when it is set not to avoid the
// person. "" when the clips under it have no subject boxes.
func textPlace(spec *timeline.Spec, lookup subject.Lookup, it timeline.TextItem, totalMs int64) string {
	if lookup == nil {
		return ""
	}
	regions := subject.Regions(spec, lookup, it.AtMs, it.AtMs+it.DurationMs)
	if len(regions) == 0 {
		return ""
	}
	if !it.AvoidsSubject() {
		cover, err := timeline.TextCovers(spec.Output, it, regions)
		switch {
		case err != nil:
			return ""
		case cover.Head:
			return hooklab.TextOnFace
		case cover.Body:
			return hooklab.TextOnBody
		}
		return hooklab.TextClear
	}
	fit, err := subject.FitText(spec, lookup, it, totalMs)
	if err != nil {
		return ""
	}
	switch fit.How {
	case subject.FitDropped:
		return hooklab.TextDropped
	case subject.FitMoved:
		return hooklab.TextMoved
	case subject.FitTrimmed:
		return hooklab.TextTrimmed
	}
	if fit.Avoidance.Overlapped {
		return hooklab.TextAdjusted
	}
	return hooklab.TextClear
}

// hookText is the text of the edit's hook: the earliest text or graphic that comes on within the first two seconds. A text that
// comes on later is not part of the hook.
func hookText(spec *timeline.Spec) (screenText, bool) {
	texts := slices.DeleteFunc(screenTexts(spec), func(t screenText) bool { return t.AtMs > hooklab.HookByMs || strings.TrimSpace(t.Text) == "" })
	if len(texts) == 0 {
		return screenText{}, false
	}
	return slices.MinFunc(texts, func(a, b screenText) int { return cmp.Compare(a.AtMs, b.AtMs) }), true
}

func (c *checker) hookScore() Check {
	w := hooklab.Resolve(nil)
	if c.in.HookWeights != nil {
		w = *c.in.HookWeights
	}
	// What the clips say is part of the hook only in a video that follows speech (hook_line applies to the same kinds).
	v, ev, ok := specOpening(c.in.Spec, openingFacts{Speech: c.in.Speech, Assets: c.in.Assets, Sharp: c.in.Sharpness, Subjects: c.in.Subjects, NoTitle: c.lim.noTitle, Silent: !speechLed(c.typ)})
	if !ok {
		return c.skip(CheckHookScore, "the edit has no shots")
	}
	card := hooklab.Score(v, ev, w.Weights)
	if card.Applied == 0 {
		return c.skip(CheckHookScore, "nothing of the opening can be measured (no text, no transcript with speech, no analysis of the first clip)")
	}
	c.m.HookScore = card.Total
	var failed []string
	for _, r := range card.Results {
		if r.Status == hooklab.StatusFail {
			failed = append(failed, fmt.Sprintf("%s (%s)", r.ID, r.Detail))
		}
	}
	summary := fmt.Sprintf("the opening scores %.2f on the hook rubric, %d of %d criteria pass", card.Total, card.Applied-card.Failed, card.Applied)
	switch {
	case card.Total < hooklab.PassTotal:
		return c.fail(CheckHookScore, "%s (at least %.2f): %s%s", summary, hooklab.PassTotal, strings.Join(failed, "; "), c.hookPickNote(v, card))
	case len(failed) > 0:
		return c.pass(CheckHookScore, "%s; weak: %s", summary, strings.Join(failed, "; "))
	}
	return c.pass(CheckHookScore, "%s", summary)
}

// shotCriteria are the criteria another first shot can fix; the others are about the text, which the plan's lab never saw.
var shotCriteria = []string{hooklab.FirstFrame, hooklab.CleanStart, hooklab.NoFillerStart, hooklab.TextOffBody}

// hookPickNote reminds the editor of the opening its plan's hook lab picked, when the edit opens on another shot and fails something a
// first shot decides ("" otherwise: a text that is too long or too late is not fixed by opening elsewhere). The pick was scored before
// the text was written, so its score is not compared with the edit's.
func (c *checker) hookPickNote(v hooklab.Variant, card hooklab.Card) string {
	if c.in.Plan == nil || c.in.Plan.HookLab == nil {
		return ""
	}
	p := c.in.Plan.HookLab
	if hooklab.SameShot(v, hooklab.Variant{AssetID: p.AssetID, InMs: p.InMs}) {
		return ""
	}
	if !slices.ContainsFunc(card.FailedIDs(), func(id string) bool { return slices.Contains(shotCriteria, id) }) {
		return ""
	}
	return fmt.Sprintf(". The plan's hook lab picked variant %s (%s from %d ms of the clip) as the first shot: open on it", p.Pick, p.AssetID, p.InMs)
}
