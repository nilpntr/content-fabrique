// Package hooklab is the hook lab (docs/design/hook-lab.md): it scores the ways a video can open against a fixed rubric and ranks
// them. A variant is one opening: the first shot, the text on it and, where the clip has speech, the line it opens on. The editor
// proposes three distinct variants; Run scores each one and returns them best first, with the pick.
//
// Scoring, ranking and proposing are pure: no I/O, no clock, no model (only LoadPlaybook and LoadCases read a file). The same rubric
// scores a proposal (plan_edit's hook lab step), the edit as it was built (check_edit's hook_score) and the eval cases
// (suites/hook-lab.yaml), so the three cannot drift apart.
//
// A criterion's detail never quotes the transcript or a text: it reaches the model, so it gives times and counts only.
package hooklab

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// The lab's sizes.
const (
	// Variants is how many variants the editor proposes.
	Variants = 3
	// MaxVariants is the most variants one Run scores.
	MaxVariants = 8
	// SameShotMs is how close the first shots of two variants may start, on one clip, before they count as the same shot.
	SameShotMs = 500
)

// Variant kinds: where a proposed variant comes from. A kind is a label for the reader; the rubric does not read it.
const (
	KindSpokenLine = "spoken_line"
	KindBestMoment = "best_moment"
	KindPlanned    = "planned"
	KindClipStart  = "clip_start"
	KindCurrent    = "current"
)

// Variant is one way to open the video.
type Variant struct {
	// ID names the variant in the result ("a", "b", "c").
	ID string `json:"id" yaml:"id"`
	// Kind says where the variant comes from (the Kind constants); empty for one a person or a model wrote.
	Kind string `json:"kind,omitempty" yaml:"kind"`
	// AssetID, InMs and OutMs are the first shot: the clip and the range of it that plays, in the clip's own milliseconds. Speed is
	// how fast it plays (0 is 1).
	AssetID string  `json:"asset_id" yaml:"asset_id"`
	InMs    int64   `json:"in_ms" yaml:"in_ms"`
	OutMs   int64   `json:"out_ms" yaml:"out_ms"`
	Speed   float64 `json:"speed,omitempty" yaml:"speed"`
	// Text is the text hook, the words on screen; empty for an opening without text. TextAtMs is when it comes on, in milliseconds
	// of the video, and TextDurationMs how long it stays (0: not decided yet, and then only its length is judged).
	Text           string `json:"text,omitempty" yaml:"text"`
	TextAtMs       int64  `json:"text_at_ms,omitempty" yaml:"text_at_ms"`
	TextDurationMs int64  `json:"text_duration_ms,omitempty" yaml:"text_duration_ms"`
}

func (v Variant) speed() float64 {
	if v.Speed <= 0 {
		return 1
	}
	return v.Speed
}

// Span is a stretch of a clip, in the clip's own milliseconds.
type Span struct {
	StartMs int64 `json:"start_ms" yaml:"start_ms"`
	EndMs   int64 `json:"end_ms" yaml:"end_ms"`
}

// Speech is what the first shot's clip says.
type Speech struct {
	Words     []transcribe.Word    `json:"words"`
	Structure transcribe.Structure `json:"structure"`
}

// Picture is what the clip analysis knows about the first shot's clip.
type Picture struct {
	// Dead are the clip's stretches of dead time (resting, walking about).
	Dead []Span `json:"dead,omitempty" yaml:"dead"`
	// Lit is the 90th percentile luma of the first frames (0 to 1), nil when it was not measured.
	Lit *float64 `json:"lit,omitempty" yaml:"lit"`
	// Sharpness is how sharp the first frames are against the sharpest of the clip (0 to 1, analysis.SharpnessScore), nil when it was
	// not measured.
	Sharpness *float64 `json:"sharpness,omitempty" yaml:"sharpness"`
}

// Where the text hook ends up against the person in the shot (Evidence.Text).
const (
	// TextClear: the text covers neither the head nor the body where it is put.
	TextClear = "clear"
	// TextAdjusted: it would cover the person, and the renderer moves or shrinks it in the frame so that it does not.
	TextAdjusted = "adjusted"
	// TextTrimmed: it fits clear of the person for a part of its time only.
	TextTrimmed = "trimmed"
	// TextMoved: it has no room at its own time, so the renderer shows it at another time.
	TextMoved = "moved"
	// TextDropped: it has no room at any time, so the renderer leaves it out.
	TextDropped = "dropped"
	// TextOnBody: it is set not to avoid the person and covers the body; TextOnFace: it covers the head.
	TextOnBody = "on_body"
	TextOnFace = "on_face"
)

// TextPlaces lists the values of Evidence.Text.
var TextPlaces = []string{TextClear, TextAdjusted, TextTrimmed, TextMoved, TextDropped, TextOnBody, TextOnFace}

// Evidence is what is known about a variant: the facts the rubric scores it on. Whatever is missing leaves its criterion "not
// applicable", which does not count for or against the variant.
type Evidence struct {
	// Speech is the transcript of the first shot's clip; nil for a clip without speech or without a transcript.
	Speech *Speech `json:"speech,omitempty"`
	// Picture is the analysis of the first shot's clip; nil for a clip that was not analysed.
	Picture *Picture `json:"picture,omitempty"`
	// Text is where the text hook ends up against the person (the Text constants); empty when the clip has no subject boxes or the
	// variant has no text.
	Text string `json:"text,omitempty"`
	// NoTitle says this creator's videos have no title: an opening without text and without speech is then not held against the
	// variant.
	NoTitle bool `json:"no_title,omitempty"`
	// TitleProbe says the variant's text is not written yet, and Text is where a title of ProbeTitle's length would end up on the first
	// shot: the room for a title is judged, when the title comes on is not.
	TitleProbe bool `json:"title_probe,omitempty"`
}

// ProbeTitle stands in for a title that is not written yet (Evidence.TitleProbe): two short words, the length of a usual hook title.
const ProbeTitle = "XXXXXX XXXXXX"

// Line is the sentence a variant opens on.
type Line struct {
	// Sentence is its index in the structure's sentences; StartMs and EndMs are its times in the clip.
	Sentence int   `json:"sentence"`
	StartMs  int64 `json:"start_ms"`
	EndMs    int64 `json:"end_ms"`
	Words    int   `json:"words"`
	// Text is the speaker's words. It is untrusted: show it to a model in a data block only.
	Text string `json:"text"`
}

// Card is a variant with its score.
type Card struct {
	Variant Variant `json:"variant"`
	// Line is the sentence the variant opens on, nil when its first shot has no speech.
	Line *Line `json:"line,omitempty"`
	// Results has one entry per criterion, in the order of Criteria.
	Results []Result `json:"results"`
	// Total is the weighted mean of the criteria that apply, 0 to 1 (0 when none applies). Applied and Failed count them.
	Total   float64 `json:"total"`
	Applied int     `json:"applied"`
	Failed  int     `json:"failed"`
}

// FailedIDs lists the criteria the variant fails.
func (c Card) FailedIDs() []string {
	var out []string
	for _, r := range c.Results {
		if r.Status == StatusFail {
			out = append(out, r.ID)
		}
	}
	return out
}

// Lab is the result of a run: the variants best first, and the pick.
type Lab struct {
	Ranked []Card `json:"ranked"`
	// Pick is the id of the best variant ("" when there are none), and Margin how far its total is above the second's (its own
	// total when it is the only one). A margin under CloseMargin is a tie in practice: taste decides.
	Pick   string  `json:"pick"`
	Margin float64 `json:"margin"`
	// Weights are the weights the criteria were scored with, and WeightsFrom where they come from ("defaults", or the playbook's
	// version).
	Weights     Weights `json:"weights"`
	WeightsFrom string  `json:"weights_from"`
}

// CloseMargin is the difference in total under which two variants are as good as each other.
const CloseMargin = 0.05

// Close says the pick is not clearly better than the second variant.
func (l Lab) Close() bool { return len(l.Ranked) > 1 && l.Margin < CloseMargin }

// Best returns the picked variant's card.
func (l Lab) Best() (Card, bool) {
	if len(l.Ranked) == 0 {
		return Card{}, false
	}
	return l.Ranked[0], true
}

// Run scores the variants and ranks them. evidence gives the facts of one variant (the same clip may back several). The order is
// deterministic: the higher total first, then the variant with fewer failing criteria, then the one with more criteria that
// apply, then the order they were given in.
func Run(variants []Variant, evidence func(Variant) Evidence, w Resolved) Lab {
	lab := Lab{Weights: w.Weights, WeightsFrom: w.From}
	if len(variants) > MaxVariants {
		variants = variants[:MaxVariants]
	}
	for _, v := range variants {
		var ev Evidence
		if evidence != nil {
			ev = evidence(v)
		}
		lab.Ranked = append(lab.Ranked, Score(v, ev, w.Weights))
	}
	slices.SortStableFunc(lab.Ranked, func(a, b Card) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.Failed, b.Failed), cmp.Compare(b.Applied, a.Applied))
	})
	if len(lab.Ranked) > 0 {
		lab.Pick, lab.Margin = lab.Ranked[0].Variant.ID, lab.Ranked[0].Total
	}
	if len(lab.Ranked) > 1 {
		lab.Margin = round3(lab.Ranked[0].Total - lab.Ranked[1].Total)
	}
	return lab
}

// Same says two variants open the same way: the same clip from (nearly) the same time, with the same text.
func Same(a, b Variant) bool {
	return a.AssetID == b.AssetID && abs(a.InMs-b.InMs) < SameShotMs && normalText(a.Text) == normalText(b.Text)
}

// Distinct returns the variants without the ones that open the same way as an earlier one.
func Distinct(variants []Variant) []Variant {
	var out []Variant
	for _, v := range variants {
		if !slices.ContainsFunc(out, func(o Variant) bool { return Same(o, v) }) {
			out = append(out, v)
		}
	}
	return out
}

// Check says what is wrong with a set of variants to run the lab on: "" when it is Variants distinct variants with ids.
func Check(variants []Variant) string {
	if len(variants) != Variants {
		return fmt.Sprintf("the hook lab takes %d variants, got %d", Variants, len(variants))
	}
	ids := map[string]bool{}
	for i, v := range variants {
		switch {
		case strings.TrimSpace(v.ID) == "" || ids[v.ID]:
			return fmt.Sprintf("variant %d needs an id of its own", i+1)
		case v.AssetID == "":
			return fmt.Sprintf("variant %s names no clip for its first shot", v.ID)
		case v.InMs < 0 || v.OutMs <= v.InMs:
			return fmt.Sprintf("variant %s: in_ms and out_ms are clip times in milliseconds with in_ms before out_ms", v.ID)
		case v.TextAtMs < 0 || v.TextDurationMs < 0:
			return fmt.Sprintf("variant %s: the text's times are not negative", v.ID)
		}
		ids[v.ID] = true
	}
	if d := Distinct(variants); len(d) != len(variants) {
		return fmt.Sprintf("the variants must differ in the first shot or in the text: %d of the %d open the same way", len(variants)-len(d), len(variants))
	}
	return ""
}

func normalText(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
