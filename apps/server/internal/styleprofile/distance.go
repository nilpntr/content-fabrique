package styleprofile

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The style distance: how far an edit is from the creator's profile, per dimension, with a one-line fix for each dimension that is out
// of range. The edit's numbers (EditStats) are measured from its timeline by the editor package; this file only compares them with the
// profile, so it has no idea what a timeline is.

// EditStats are the measures of an edit that the profile has a counterpart for.
type EditStats struct {
	DurationS float64
	// ShotsS are the on-screen lengths of the shots (a transition's overlap is not counted).
	ShotsS []float64
	// FirstCutS is the time of the first cut, -1 when there is none.
	FirstCutS float64
	// Effects counts the motion presets, the transitions that are not cuts and the speed ramps.
	Effects int
	// Titles counts the texts that are titles and Labels the small labels (every text beyond the first title, and the label styles).
	Titles, Labels int
	// TitleFill is the first title's fill as "#RRGGBB", TitleLines its number of lines, TitleHeightPct the height of its capitals as a
	// percentage of the frame height and TitleShadow its shadow (none, soft or strong); all zero when it was not measured.
	TitleFill      string
	TitleLines     int
	TitleHeightPct float64
	TitleShadow    string
	// TitleScale is the scale override of the title (1 when it has none).
	TitleScale float64
	// TitleChars is the length of the title in characters (line breaks count as spaces), TitleBreak says the text has a line break of its
	// own, TitleFitScale is the largest scale at which the title fits one line when it wraps (0 when none from 0.6 up does), and TitleKind
	// is its shape (KindSeries, ...).
	TitleChars    int
	TitleBreak    bool
	TitleFitScale float64
	TitleKind     string
	// TitleCase is "upper", "lower" or "mixed" for the first title ("" when there is none); TitleAtS is when it starts.
	TitleCase string
	TitleAtS  float64
	// Energetic says the edit's vibe is lively or hype: its pace is then held by the pace targets of the editor's check (from the creator's
	// measured shot lengths, scaled by the vibe), not by the cut count of their videos, so the pacing dimensions (cuts, long takes, shot
	// length) do not apply. A creator whose videos are one or two long takes and a few cuts would otherwise forbid the pace of their own
	// cut-up videos.
	Energetic bool
	// Graded says some clip has a look other than none; SFX counts the sound effects; Captions says the captions are on.
	Graded   bool
	SFX      int
	Captions bool
}

// CaseOf is the case of a text: upper when it has letters and no lowercase ones, lower when none is uppercase, otherwise mixed.
func CaseOf(text string) string {
	hasUpper, hasLower := false, false
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z' || (r > 127 && strings.ToUpper(string(r)) == string(r) && strings.ToLower(string(r)) != string(r)):
			hasUpper = true
		case r >= 'a' && r <= 'z' || (r > 127 && strings.ToLower(string(r)) == string(r) && strings.ToUpper(string(r)) != string(r)):
			hasLower = true
		}
	}
	switch {
	case hasUpper && !hasLower:
		return "upper"
	case hasLower && !hasUpper:
		return "lower"
	}
	return "mixed"
}

// Dimension is one line of the distance.
type Dimension struct {
	ID       string `json:"id"`
	Value    string `json:"value"`
	Expected string `json:"expected"`
	OK       bool   `json:"ok"`
	// Fix is a one-line instruction for a dimension that is out of range.
	Fix string `json:"fix,omitempty"`
	// Soft marks a dimension that only advises: out of range it is borderline, and does not make the edit fail.
	Soft bool `json:"soft,omitempty"`
}

// Distance is the result of comparing an edit with a profile.
type Distance struct {
	Dimensions []Dimension `json:"dimensions"`
	// Score is the share of the dimensions that are in range (1: the edit is in the creator's style on every dimension we measure).
	Score float64 `json:"score"`
}

// Failed lists the dimensions that are out of range.
func (d Distance) Failed() []Dimension {
	var out []Dimension
	for _, x := range d.Dimensions {
		if !x.OK && !x.Soft {
			out = append(out, x)
		}
	}
	return out
}

// Hard is how many dimensions have to match (the soft ones only advise).
func (d Distance) Hard() int {
	n := 0
	for _, x := range d.Dimensions {
		if !x.Soft {
			n++
		}
	}
	return n
}

// Borderline lists the soft dimensions that are out of range: advice, not a failure.
func (d Distance) Borderline() []Dimension {
	var out []Dimension
	for _, x := range d.Dimensions {
		if !x.OK && x.Soft {
			out = append(out, x)
		}
	}
	return out
}

func inRange(v, lo, hi float64) bool { return v >= lo && v <= hi }

// Compare measures how far the edit is from the profile.
func (p *Profile) Compare(e EditStats) Distance {
	var dims []Dimension
	add := func(id, value, expected string, ok bool, fix string) {
		if ok {
			fix = ""
		}
		dims = append(dims, Dimension{ID: id, Value: value, Expected: expected, OK: ok, Fix: fix})
	}
	addSoft := func(id, value, expected string, ok bool, fix string) {
		add(id, value, expected, ok, fix)
		dims[len(dims)-1].Soft = true
	}
	n := len(e.ShotsS)
	cuts := max(n-1, 0)
	pc := p.Pacing

	// Cuts: the count a video has, and the pace.
	th := p.Thresholds()
	lo, hi := th.CutsLo, th.CutsHi
	perMin := 0.0
	if e.DurationS > 0 {
		perMin = float64(cuts) / e.DurationS * 60
	}
	if !e.Energetic {
		add("cuts", fmt.Sprintf("%d cuts (%.0f a minute)", cuts, perMin), fmt.Sprintf("%.0f to %.0f cuts, %s a minute", lo, hi, pc.CutsPerMinute),
			inRange(float64(cuts), lo, hi) && perMin <= th.CutsPerMinHi,
			cutsFix(pc, float64(cuts), perMin, lo, hi, th.CutsPerMinHi))
	}

	// Long takes: when the creator lets takes run, so should the edit (at a calm vibe; lively and hype have the pace targets).
	switch {
	case e.Energetic: // the pace targets of the editor's check hold the shot lengths
	case pc.HasLongTakes:
		longest := 0.0
		for _, s := range e.ShotsS {
			longest = math.Max(longest, s)
		}
		need := th.LongTakeNeedS
		add("long_takes", fmt.Sprintf("longest shot %.1f s", longest), fmt.Sprintf("a shot of %.0f s or more (the creator's longest is %s s)", need, pc.LongestShotS),
			longest >= need, fmt.Sprintf("keep one take running for %.0f s or more (a full set), instead of cutting it up", need))
	case pc.ShotMedianS.Median > 0:
		shorts := slices.Clone(e.ShotsS)
		slices.Sort(shorts)
		med := 0.0
		if len(shorts) > 0 {
			med = shorts[len(shorts)/2]
		}
		add("shot_length", fmt.Sprintf("median shot %.1f s", med), fmt.Sprintf("about %.1f s (%.1f to %.1f)", pc.ShotMedianS.Median, th.ShotLenLoS, th.ShotLenHiS),
			inRange(med, th.ShotLenLoS, th.ShotLenHiS), "match the creator's shot lengths")
	}

	// The hook: where the first cut comes.
	if pc.CutsPerVideo.Max > 0 && p.Hook.FirstCutS.Max > 0 && e.FirstCutS >= 0 {
		flo, fhi := th.FirstCutLoS, th.FirstCutHiS
		add("first_cut", fmt.Sprintf("first cut at %.1f s", e.FirstCutS), fmt.Sprintf("%.1f to %.1f s", flo, fhi), inRange(e.FirstCutS, flo, fhi),
			fmt.Sprintf("make the opening shot about %s s long, the creator's first cut", p.Hook.FirstCutS))
	}

	// Effects.
	perMinFx := 0.0
	if e.DurationS > 0 {
		perMinFx = float64(e.Effects) / e.DurationS * 60
	}
	if p.Effects.Verdict == EffectsNone {
		add("effects", fmt.Sprintf("%d effects", e.Effects), "none (the creator uses no effects)", e.Effects == 0,
			"take the motion presets, transitions and speed ramps out: the creator's videos have none")
	} else {
		fhi := th.FxPerMinHi
		add("effects", fmt.Sprintf("%.1f a minute", perMinFx), fmt.Sprintf("up to %.1f a minute", fhi), perMinFx <= fhi, "use fewer effects")
	}

	// Text: one title, no labels; the case of it.
	if p.Text != nil {
		titles := p.Text.TitleShare > 0
		add("title", fmt.Sprintf("%d titles", e.Titles), map[bool]string{true: "one title", false: "no title"}[titles],
			(titles && e.Titles == 1) || (!titles && e.Titles == 0), map[bool]string{true: "use exactly one title", false: "leave the title out"}[titles])
		if titles && e.Titles >= 1 {
			p.compareTitleLook(e, add, addSoft)
		}
		if p.Text.LabelShare == 0 {
			add("labels", fmt.Sprintf("%d labels", e.Labels), "none (the creator uses no exercise labels)", e.Labels == 0, "take the exercise labels out: the creator has one title only")
		}
		if titles && p.Text.Case != "" && p.Text.Case != "mixed" && e.TitleCase != "" {
			add("title_case", e.TitleCase, p.Text.Case, e.TitleCase == p.Text.Case, fmt.Sprintf("write the title in %s case", p.Text.Case))
		}
		if p.Text.CaptionShare == 0 {
			add("captions", map[bool]string{true: "on", false: "off"}[e.Captions], "off", !e.Captions, "turn the captions off")
		}
	}

	// Look and sound effects.
	if p.Look.Grade == "none" {
		add("grade", map[bool]string{true: "a look is set", false: "no look"}[e.Graded], "no look (the creator posts the natural picture)", !e.Graded, "set the look of every clip to none")
	}
	if p.Audio.Content == ContentMusicOnly {
		add("sfx", fmt.Sprintf("%d sound effects", e.SFX), "none (music only)", e.SFX == 0, "remove the sound effects: the creator's videos are music only")
	}

	// Length.
	if p.Format.DurationS.Max > 0 {
		dlo, dhi := th.DurLoS, th.DurHiS
		add("duration", fmt.Sprintf("%.1f s", e.DurationS), fmt.Sprintf("%.0f to %.0f s", dlo, dhi), inRange(e.DurationS, dlo, dhi), "bring the length into the creator's range")
	}

	ok, hard := 0, 0
	for _, d := range dims {
		if d.Soft {
			continue // advice does not count in the share
		}
		hard++
		if d.OK {
			ok++
		}
	}
	score := 1.0
	if hard > 0 {
		score = round2(float64(ok) / float64(hard))
	}
	return Distance{Dimensions: dims, Score: score}
}

// DeltaEMax is the colour difference (CIE76) up to which a title colour counts as the creator's.
const DeltaEMax = 12.0

// The share of the creator's letter height the title's may be within: 70 % to 135 %.
const (
	TitleSizeLo = 0.70
	TitleSizeHi = 1.35
)

// shadowRank orders the shadows: none, soft, strong.
func shadowRank(s string) int {
	switch s {
	case "strong":
		return 2
	case "soft":
		return 1
	}
	return 0
}

// compareTitleLook adds the dimensions of the title's look: its colour (the colour difference), its lines, the size of its letters and
// its shadow, each only when the profile and the edit both have the number.
func (p *Profile) compareTitleLook(e EditStats, add, addSoft func(id, value, expected string, ok bool, fix string)) {
	t := p.Text
	if hexColour6.MatchString(t.Colour) && hexColour6.MatchString(e.TitleFill) {
		de, _ := DeltaE(t.Colour, e.TitleFill)
		add("title_colour", fmt.Sprintf("%s (colour difference %.0f)", strings.ToUpper(e.TitleFill), de), fmt.Sprintf("%s within %.0f", t.Colour, DeltaEMax), de <= DeltaEMax,
			fmt.Sprintf("set the title's fill to %s", t.Colour))
	}
	// One line: when most of the creator's titles are one line, so is this one, however long (a title over the limit is shortened); when only
	// some are, a title of the length of theirs that were one line is.
	wantOne := t.OneLineShare >= 0.5 || (t.OneLineChars > 0 && e.TitleChars > 0 && e.TitleChars <= t.OneLineChars)
	if wantOne && e.TitleLines > 0 {
		expect := "one line (the creator's titles of this length are one line)"
		if t.OneLineShare >= 0.5 {
			expect = "one line (the creator's titles are one line)"
		}
		add("title_lines", fmt.Sprintf("%d lines (%d characters)", e.TitleLines, e.TitleChars), expect, e.TitleLines == 1, titleLinesFix(e, t))
	}
	if patternKinds(t) != "" && e.TitleKind != "" {
		addSoft("title_pattern", e.TitleKind, "one of the creator's forms: "+patternKinds(t), t.Follows(e.TitleKind),
			titlePatternFix(t))
	}
	if t.HeightPct > 0 && e.TitleHeightPct > 0 {
		ratio := e.TitleHeightPct / t.HeightPct
		add("title_size", fmt.Sprintf("capitals %.1f %% of the frame height", e.TitleHeightPct), fmt.Sprintf("about %.1f %% (%.1f to %.1f)", t.HeightPct, t.HeightPct*TitleSizeLo, t.HeightPct*TitleSizeHi),
			ratio >= TitleSizeLo && ratio <= TitleSizeHi, titleSizeFix(e, ratio, t.HeightPct))
	}
	if t.Shadow != "" && e.TitleShadow != "" {
		add("title_shadow", e.TitleShadow, "at most "+t.Shadow, shadowRank(e.TitleShadow) <= shadowRank(t.Shadow),
			fmt.Sprintf("set the title's shadow to %s", t.Shadow))
	}
}

// titleLinesFix says how to make the title one line: take the line break out, lower the scale to the one that fits, or shorten the text.
func titleLinesFix(e EditStats, t *TextProfile) string {
	switch {
	case t.OneLineChars > 0 && e.TitleChars > t.OneLineChars:
		fix := fmt.Sprintf("shorten to %d characters or fewer so it fits on one line", t.OneLineChars)
		if e.TitleBreak {
			fix += ", and take the line break out of the text"
		}
		return fix
	case e.TitleBreak:
		return "take the line break out of the title's text so it is one line"
	case e.TitleFitScale > 0 && fitsTheCreatorsSize(e, t):
		return fmt.Sprintf("set the title's scale to %g to fit it on one line, or shorten the text", e.TitleFitScale)
	}
	return "shorten the title's text to fit one line (a smaller scale would make the letters smaller than the creator's)"
}

// fitsTheCreatorsSize says the scale at which the title fits one line still gives capitals within the creator's size range.
func fitsTheCreatorsSize(e EditStats, t *TextProfile) bool {
	if t.HeightPct <= 0 || e.TitleHeightPct <= 0 {
		return true
	}
	cur := e.TitleScale
	if cur <= 0 {
		cur = 1
	}
	return e.TitleHeightPct*e.TitleFitScale/cur >= t.HeightPct*TitleSizeLo
}

// patternKinds lists the kinds the creator uses, for the expected column.
func patternKinds(t *TextProfile) string {
	var ks []string
	for _, p := range t.Patterns {
		if p.Share >= PatternMinShare {
			ks = append(ks, p.Kind)
		}
	}
	return strings.Join(ks, ", ")
}

// titlePatternFix names the creator's most common form without quoting their words.
func titlePatternFix(t *TextProfile) string {
	if len(t.Patterns) == 0 {
		return ""
	}
	p := t.Patterns[0]
	s := "write a new title as " + kindWords(p.Kind) + ", " + p.Template()
	if p.Number != "" {
		s += ", ending in " + p.Number
	}
	return s
}

// titleSizeFix says what scale to set, as a number in the range the ops take (0.6 to 1.4, from the title's own scale and how far off it is).
func titleSizeFix(e EditStats, ratio, target float64) string {
	cur := e.TitleScale
	if cur == 0 {
		cur = 1
	}
	want := math.Round(math.Min(math.Max(cur/ratio, style.MinScale), style.MaxScale)*100) / 100
	return fmt.Sprintf("set the title's scale to %g to make the capitals about %.1f %% of the frame height", want, target)
}

// cutsFix is the one-line fix for the cuts dimension, chosen from the bound that failed: too many cuts, a pace that is too fast for
// the number of cuts, or too few cuts.
func cutsFix(pc PacingProfile, cuts, perMin, lo, hi, perMinHi float64) string {
	switch {
	case cuts > hi:
		return fmt.Sprintf("the creator cuts %s times a video: use fewer cuts, merge shots and let the takes run", pc.CutsPerVideo)
	case cuts < lo:
		return fmt.Sprintf("the creator cuts %s times a video: add cuts", pc.CutsPerVideo)
	case perMin > perMinHi:
		return fmt.Sprintf("the creator cuts %s a minute: use fewer cuts or longer takes", pc.CutsPerMinute)
	}
	return fmt.Sprintf("the creator cuts %s times a video: match the number and the pace of their cuts", pc.CutsPerVideo)
}

// Text is the distance as the editor and cfctl read it.
func (d Distance) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "style distance: %.0f %% of %d dimensions in the creator's range\n", d.Score*100, d.Hard())
	for _, x := range d.Dimensions {
		mark := "ok  "
		switch {
		case !x.OK && x.Soft:
			mark = "adv " // advice: borderline, not a failure
		case !x.OK:
			mark = "OUT "
		}
		fmt.Fprintf(&b, "%s %s: %s (expected %s)", mark, x.ID, x.Value, x.Expected)
		if x.Fix != "" {
			b.WriteString(". " + x.Fix)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
