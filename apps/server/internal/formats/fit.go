package formats

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// Fit: which formats a project's footage can make, from what the editor already knows of the clips (their analysis: reps and sets; their
// titles and descriptions, which the quick look wrote or the creator typed; the brief). The words are only counted, never passed on.

// The footage signals a card may need or be helped by.
const (
	// SignalReps: a clip with a set of three reps or more. SignalTwoExercises, SignalThreeExercises: two, three such clips.
	SignalReps           = "reps"
	SignalTwoExercises   = "two_exercises"
	SignalThreeExercises = "three_exercises"
	// SignalBigSet: a clip with six reps or more (a set worth counting).
	SignalBigSet = "big_set"
	// SignalPose: a pose or physique clip.
	SignalPose = "pose"
	// SignalThenClip: an old clip or photo of the creator (a "before"), named so in the clips' words or the brief, or a picture.
	SignalThenClip = "then_clip"
	// SignalTalking: someone speaks to the camera.
	SignalTalking = "talking"
	// SignalProduct: a product is shown.
	SignalProduct = "product"
	// SignalLifestyle: two clips or more that are not training or posing (the street, the kitchen, the car).
	SignalLifestyle = "lifestyle"
	// SignalClips2, SignalClips3, SignalManyClips: at least two, three, five video clips.
	SignalClips2    = "clips_2"
	SignalClips3    = "clips_3"
	SignalManyClips = "many_clips"
	// SignalLongClip: a clip of 8 s or more.
	SignalLongClip = "long_clip"
	// SignalSong: the creator gave a song for the edit.
	SignalSong = "song"
)

// Signals lists the footage signals.
var Signals = []string{
	SignalReps, SignalTwoExercises, SignalThreeExercises, SignalBigSet, SignalPose, SignalThenClip, SignalTalking, SignalProduct,
	SignalLifestyle, SignalClips2, SignalClips3, SignalManyClips, SignalLongClip, SignalSong,
}

// signalWords say a signal in words, for the editor ("2 clips with sets").
var signalWords = map[string]string{
	SignalReps: "a clip with a set", SignalTwoExercises: "two clips with sets", SignalThreeExercises: "three clips with sets",
	SignalBigSet: "a set of six reps or more", SignalPose: "a pose or physique clip", SignalThenClip: "an old clip or photo (a before)",
	SignalTalking: "someone talking to the camera", SignalProduct: "a product", SignalLifestyle: "lifestyle clips (not training)",
	SignalClips2: "two clips or more", SignalClips3: "three clips or more", SignalManyClips: "five clips or more",
	SignalLongClip: "a clip of 8 s or more", SignalSong: "a song",
}

// SignalWords is a signal in words.
func SignalWords(s string) string {
	if w, ok := signalWords[s]; ok {
		return w
	}
	return s
}

// Clip is what fit reads of one clip of the footage.
type Clip struct {
	// Kind is video, audio or image.
	Kind string
	// Words are the clip's title and description (untrusted: only counted).
	Words      string
	DurationMs int64
	// Reps is the reps the analysis found in the clip, Analysed whether it has an analysis.
	Reps     int
	Analysed bool
	// Pose says the clip's labels mark a pose or physique shot.
	Pose bool
}

// Footage is a project's footage for fit: its clips and the brief.
type Footage struct {
	Clips []Clip
	Brief string
}

var (
	thenWords      = []string{"before", "old", "throwback", "then", "years ago", "year ago", "back then", "transformation", "progress", "skinny", "first day", "day 1", "younger"}
	talkWords      = []string{"talking", "talks", "talk", "speaking", "speaks", "explaining", "explains", "to camera", "to the camera", "podcast", "interview", "monologue", "voiceover"}
	productWords   = []string{"product", "supplement", "shaker", "protein", "pre workout", "creatine", "bottle", "shoes", "sneakers", "outfit", "gear", "belt", "straps", "unboxing", "package", "packaging", "bag", "watch", "headphones"}
	lifestyleWords = []string{"street", "kitchen", "car", "walk", "walking", "coffee", "breakfast", "meal", "cooking", "city", "home", "office", "morning", "outside", "beach", "travel", "friends", "mirror", "selfie"}
)

// SignalsOf are the signals that hold for the footage.
func SignalsOf(f Footage) map[string]bool {
	s := map[string]bool{}
	video, repClips, lifestyle := 0, 0, 0
	for _, c := range f.Clips {
		switch c.Kind {
		case "audio":
			s[SignalSong] = true
			continue
		case "image":
			s[SignalThenClip] = s[SignalThenClip] || hasKeyword(c.Words, thenWords)
			continue
		}
		video++
		if c.Reps >= 3 {
			repClips++
		}
		if c.Reps >= 6 {
			s[SignalBigSet] = true
		}
		if c.Pose {
			s[SignalPose] = true
		}
		if c.DurationMs >= 8000 {
			s[SignalLongClip] = true
		}
		if hasKeyword(c.Words, thenWords) {
			s[SignalThenClip] = true
		}
		if hasKeyword(c.Words, talkWords) {
			s[SignalTalking] = true
		}
		if hasKeyword(c.Words, productWords) {
			s[SignalProduct] = true
		}
		if !c.Pose && (c.Analysed && c.Reps == 0 || hasKeyword(c.Words, lifestyleWords)) && c.Reps < 3 {
			lifestyle++
		}
	}
	s[SignalReps] = repClips >= 1
	s[SignalTwoExercises] = repClips >= 2
	s[SignalThreeExercises] = repClips >= 3
	s[SignalLifestyle] = lifestyle >= 2
	s[SignalClips2] = video >= 2
	s[SignalClips3] = video >= 3
	s[SignalManyClips] = video >= 5
	// The brief can name what the clips' words do not: an old clip, someone talking, a product.
	if hasKeyword(f.Brief, thenWords) {
		s[SignalThenClip] = true
	}
	if hasKeyword(f.Brief, productWords) {
		s[SignalProduct] = true
	}
	return s
}

// Fit is a format against a project's footage and a creator.
type Fit struct {
	Card Card
	// Fits is false when a signal the card needs is missing (Missing lists them, in words).
	Fits    bool
	Missing []string
	// Has lists the signals that help and hold, in words.
	Has []string
	// FitScore is how well the footage fits (0 to 1), CreatorShare the share of the creator's videos in this format, Rank the two
	// together.
	FitScore     float64
	CreatorShare float64
	CreatorUses  bool
	Rank         float64
	// New says the creator has not used this format (when their formats are known), Novel that it is put forward as the "new to you"
	// idea of this edit.
	New, Novel bool
	// Excluded says this edit may not use the format (an earlier version of the project used it, and the creator asked for a new idea).
	Excluded bool
}

// RankOptions are what decides the order besides the footage.
type RankOptions struct {
	// ContentType is the likely kind of video ("" when unknown).
	ContentType string
	// Creator is the creator's formats (nil when their formats are not known: then no format is "new to you").
	Creator []Share
	// Exclude are formats this edit may not use (a new idea: the formats of the earlier versions).
	Exclude []string
	// Novel is how many formats the creator has not used to put forward (1 or 2).
	Novel int
	// Vibe is the edit's vibe: a format that plays weak at it ranks lower.
	Vibe string
}

// Rank returns every card scored against the footage, the formats that fit first by rank, then the devices, then what does not fit.
func (l *Library) Rank(f Footage, opts RankOptions) []Fit {
	sig := SignalsOf(f)
	share := map[string]float64{}
	for _, s := range opts.Creator {
		share[s.ID] = s.Share
	}
	var out []Fit
	for _, c := range l.cards {
		fit := Fit{Card: c, Fits: true}
		for _, n := range c.Fits.Needs {
			if !sig[n] {
				fit.Fits = false
				fit.Missing = append(fit.Missing, SignalWords(n))
			}
		}
		if len(c.Fits.NeedsAny) > 0 && !slices.ContainsFunc(c.Fits.NeedsAny, func(n string) bool { return sig[n] }) {
			fit.Fits = false
			var alts []string
			for _, n := range c.Fits.NeedsAny {
				alts = append(alts, SignalWords(n))
			}
			fit.Missing = append(fit.Missing, strings.Join(alts, " or "))
		}
		score := 0.0
		if fit.Fits {
			score = 0.6
			helps := 0
			for _, h := range c.Fits.Helps {
				if sig[h] {
					helps++
					fit.Has = append(fit.Has, SignalWords(h))
				}
			}
			if len(c.Fits.Helps) > 0 {
				score += 0.25 * float64(helps) / float64(len(c.Fits.Helps))
			}
			if opts.ContentType != "" && slices.Contains(c.Fits.ContentTypes, opts.ContentType) {
				score += 0.15
			}
			if opts.Vibe != "" && c.Vibes[opts.Vibe] == VibeWeak {
				score -= 0.15
			}
			for _, n := range append(slices.Clone(c.Fits.Needs), c.Fits.NeedsAny...) {
				if sig[n] {
					fit.Has = append([]string{SignalWords(n)}, fit.Has...)
				}
			}
		}
		fit.FitScore = math.Round(math.Max(score, 0)*100) / 100
		fit.CreatorShare = share[c.ID]
		fit.CreatorUses = fit.CreatorShare > 0
		fit.New = opts.Creator != nil && !fit.CreatorUses && c.Kind == KindFormat
		fit.Rank = math.Round((0.6*fit.FitScore+0.4*fit.CreatorShare)*100) / 100
		fit.Excluded = slices.Contains(opts.Exclude, c.ID)
		out = append(out, fit)
	}
	group := func(f Fit) int {
		switch {
		case f.Fits && f.Card.Kind == KindFormat:
			return 0
		case f.Fits:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if gi, gj := group(out[i]), group(out[j]); gi != gj {
			return gi < gj
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		return out[i].Card.ID < out[j].Card.ID
	})
	// The "new to you" ideas: the best-fitting formats the creator has not used, not excluded.
	novel := opts.Novel
	if novel <= 0 {
		novel = 2
	}
	for i := range out {
		if novel == 0 {
			break
		}
		if out[i].Fits && out[i].New && !out[i].Excluded {
			out[i].Novel = true
			novel--
		}
	}
	return out
}

// Describe is a card in a few lines for the editor: the pitch, the beats, the text, the music, the length and pace, the vibes and how
// it plays.
func (c Card) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "   %s\n", c.Pitch)
	for _, beat := range c.Beats {
		fmt.Fprintf(&b, "   %s (%s; roles %s): %s\n", beat.Name, beat.Timing, strings.Join(beat.Roles, "/"), beat.What)
	}
	text := strings.ReplaceAll(c.Text.Pattern, "_", " ")
	if len(c.Text.TitleForms) > 0 {
		var forms []string
		for _, f := range c.Text.TitleForms {
			forms = append(forms, fmt.Sprintf("%q", f))
		}
		text += ", title forms " + strings.Join(forms, ", ")
	}
	fmt.Fprintf(&b, "   text: %s. %s\n", text, c.Text.Notes)
	fmt.Fprintf(&b, "   music: %s\n", c.Music)
	fmt.Fprintf(&b, "   length %d to %d s, pace %s: %s\n", c.Length.Min, c.Length.Max, c.Pace, c.PaceHint)
	fmt.Fprintf(&b, "   vibes: calm %s, lively %s, hype %s\n", c.Vibes["calm"], c.Vibes["lively"], c.Vibes["hype"])
	fmt.Fprintf(&b, "   not when: %s\n", strings.Join(c.NotWhen, " "))
	for _, h := range c.HowItPlays {
		fmt.Fprintf(&b, "   how it plays: %s\n", h)
	}
	return b.String()
}
