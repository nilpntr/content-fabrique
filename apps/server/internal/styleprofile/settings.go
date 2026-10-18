package styleprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The creator's settings (P6-05b, docs/design/style-profile-v1.md section 11): the choices a creator makes about their edits, kept apart
// from the measures so that learning the style again never overwrites them. The editor follows an effective style made of three layers,
// the creator always winning: what was measured from their videos, then the defaults the AI proposed from those measures, then the
// creator's own settings. A measured "no effects" is a fact about their videos, not a rule: it never turns transitions, text animation
// or camera motion off by itself when the creator or the vibe asks for them.

// The vibes: the energy of an edit. The same values as an editor run's own vibe (P6-06).
const (
	VibeCalm    = "calm"
	VibeLively  = "lively"
	VibeHype    = "hype"
	DefaultVibe = VibeLively
)

// Vibes lists the vibes, calm to hype.
var Vibes = []string{VibeCalm, VibeLively, VibeHype}

// The limits of the reference videos in the app (P6-05b): at most MaxReferences (not failed) per workspace, each a video of at most
// MaxReferenceBytes and MaxReferenceDuration.
const (
	MaxReferenceBytes    = 1 << 30
	MaxReferenceDuration = 3 * time.Minute
)

// ValidVibe says whether v is a vibe ("" is not).
func ValidVibe(v string) bool { return slices.Contains(Vibes, v) }

// The values of the settings.
const (
	LevelOff    = "off"
	LevelSubtle = "subtle"
	LevelLots   = "lots"
	LevelOn     = "on"

	PositionTop        = "top"
	PositionUpperThird = "upper_third"
	PositionAuto       = "auto"

	CaseUpper   = "upper"
	CaseLower   = "lower"
	CaseAsTyped = "as_typed"

	MusicAlways    = "always"
	MusicWhenAdded = "when_added"
	MusicNever     = "never"

	PaceSlower   = "slower"
	PaceLikeMine = "like_mine"
	PaceFaster   = "faster"

	HoldShort = "short"
	HoldLong  = "long"

	CutsOnFullReps = "full_reps"
	CutsMidRep     = "mid_rep"
)

// The allowed values of each setting, by its JSON name.
var settingValues = map[string][]string{
	"vibe":           Vibes,
	"transitions":    {LevelOff, LevelSubtle, LevelLots},
	"text_animation": {LevelOff, LevelOn},
	"camera_motion":  {LevelOff, LevelSubtle, LevelLots},
	"title_position": {PositionTop, PositionUpperThird, PositionAuto},
	"title_case":     {CaseUpper, CaseLower, CaseAsTyped},
	"music":          {MusicAlways, MusicWhenAdded, MusicNever},
	"pace":           {PaceSlower, PaceLikeMine, PaceFaster},
	"chapter_titles": {LevelOff, LevelOn},
	"ending_hold":    {HoldShort, HoldLong},
	"cut_timing":     {CutsOnFullReps, CutsMidRep},
	"angle_switches": {LevelOff, LevelOn},
}

// SettingValues returns the allowed values of a setting (nil for title_colour, a hex code, and for an unknown name).
func SettingValues(name string) []string { return slices.Clone(settingValues[name]) }

// Settings are the creator's choices. An empty field is not set: the layer below decides it.
type Settings struct {
	Vibe          string `json:"vibe,omitempty"`
	Transitions   string `json:"transitions,omitempty"`
	TextAnimation string `json:"text_animation,omitempty"`
	CameraMotion  string `json:"camera_motion,omitempty"`
	// TitleColour is "#RRGGBB" in capitals.
	TitleColour   string `json:"title_colour,omitempty"`
	TitlePosition string `json:"title_position,omitempty"`
	TitleCase     string `json:"title_case,omitempty"`
	Music         string `json:"music,omitempty"`
	Pace          string `json:"pace,omitempty"`
	// ChapterTitles is whether a video of several exercises gets numbered chapter titles; TitleTemplate the form of the titles, with
	// slots such as <TOPIC> and <N> (the creator's own text when they set it: it reaches the editor as data); EndingHold whether the last
	// shot is held; CutTiming whether cuts fall on the end of a rep or in the middle of one; AngleSwitches whether one exercise switches
	// between angles (P6-05c).
	ChapterTitles string `json:"chapter_titles,omitempty"`
	TitleTemplate string `json:"title_template,omitempty"`
	EndingHold    string `json:"ending_hold,omitempty"`
	CutTiming     string `json:"cut_timing,omitempty"`
	AngleSwitches string `json:"angle_switches,omitempty"`
}

// fields lists the settings with their JSON names, for validation and resolution.
func (s *Settings) fields() []struct {
	name string
	v    *string
} {
	return []struct {
		name string
		v    *string
	}{
		{"vibe", &s.Vibe}, {"transitions", &s.Transitions}, {"text_animation", &s.TextAnimation}, {"camera_motion", &s.CameraMotion},
		{"title_colour", &s.TitleColour}, {"title_position", &s.TitlePosition}, {"title_case", &s.TitleCase}, {"music", &s.Music}, {"pace", &s.Pace},
		{"chapter_titles", &s.ChapterTitles}, {"title_template", &s.TitleTemplate}, {"ending_hold", &s.EndingHold}, {"cut_timing", &s.CutTiming},
		{"angle_switches", &s.AngleSwitches},
	}
}

// ErrInvalidSettings is wrapped by every refusal of Normalize and ParseSettings.
var ErrInvalidSettings = errors.New("styleprofile: the style settings are not valid")

// Normalize checks every setting (empty or one of its values; the colour a #RRGGBB hex code) and returns them with the colour in capitals.
func (s Settings) Normalize() (Settings, error) {
	out := s
	for _, f := range out.fields() {
		*f.v = strings.TrimSpace(*f.v)
		if *f.v == "" {
			continue
		}
		if f.name == "title_template" {
			*f.v = strings.Join(strings.Fields(*f.v), " ")
			if !ValidTemplate(*f.v) || suspicious.MatchString(strings.ToLower(*f.v)) {
				return Settings{}, fmt.Errorf("%w: title_template must be a short title form such as <TOPIC> <N> (letters, digits, spaces, slots)", ErrInvalidSettings)
			}
			continue
		}
		if f.name == "title_colour" {
			if !hexColour.MatchString(*f.v) {
				return Settings{}, fmt.Errorf("%w: title_colour must be a colour like #RRGGBB", ErrInvalidSettings)
			}
			*f.v = strings.ToUpper(*f.v)
			continue
		}
		if !slices.Contains(settingValues[f.name], *f.v) {
			return Settings{}, fmt.Errorf("%w: %s must be one of %s", ErrInvalidSettings, f.name, strings.Join(settingValues[f.name], ", "))
		}
	}
	return out, nil
}

// IsZero says no setting is set.
func (s Settings) IsZero() bool { return s == Settings{} }

// ParseSettings decodes stored or requested settings strictly (an unknown field is refused) and normalises them. Empty input is no
// settings.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Settings{}, nil
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	return s.Normalize()
}

// Where an effective setting came from.
const (
	// SourceThisVideo: the vibe picked for this one edit.
	SourceThisVideo = "this_video"
	// SourceYou: the creator's own setting.
	SourceYou = "you"
	// SourceSuggested: the default the AI proposed from the creator's videos.
	SourceSuggested = "suggested"
	// SourceMeasured: measured from the creator's videos.
	SourceMeasured = "measured"
	// SourceVibe: what the vibe asks for.
	SourceVibe = "vibe"
	// SourceDefault: the app's default.
	SourceDefault = "default"
)

// Resolved is the effective style: every setting decided, with where each came from (Sources has the same fields, holding a Source
// constant). TitleColour may stay empty: then nothing decides the colour and the editor uses the style's own.
type Resolved struct {
	Settings
	Sources Settings `json:"sources"`
}

// vibeDefaults is what a vibe asks for, of the settings that move: calm keeps close to restraint, lively and hype move. No vibe asks for
// transitions: creators read effect transitions (punches, zooms, whips) as the mark of an automatic AI edit, so the energy of a vibe
// comes from the cuts on the beat, the pace, the camera and the text, and transitions come only from the creator's own videos or their
// setting (Resolve).
var vibeDefaults = map[string]Settings{
	VibeCalm:   {Transitions: LevelOff, TextAnimation: LevelOn, CameraMotion: LevelSubtle},
	VibeLively: {Transitions: LevelOff, TextAnimation: LevelOn, CameraMotion: LevelSubtle},
	VibeHype:   {Transitions: LevelOff, TextAnimation: LevelOn, CameraMotion: LevelLots},
}

// Energetic says the vibe asks for motion whatever the creator's measured effects: lively and hype.
func Energetic(vibe string) bool { return vibe == VibeLively || vibe == VibeHype }

// Resolve makes the effective style: the measures of the profile (nil for none), then the AI's suggestion (nil for none), then the
// creator's settings, and for one edit the vibe chosen for it (runVibe, "" for none). The creator always wins, and:
//
//   - The vibe: this edit's, else the creator's, else the suggestion's, else lively.
//   - Transitions: the creator's; else, when their videos measurably use transitions, the suggestion (when it is not off) or the level
//     measured; else off, at every vibe. A vibe never asks for transitions.
//   - Text animation and camera motion: the creator's, else (when the vibe was chosen by a person, for this edit or in the settings)
//     what that vibe asks for, else the suggestion, else the vibe's. A suggestion of "off" does not hold at a lively or hype vibe: the
//     measured "no effects" it may come from is not a reason to make a lively edit static.
//   - The title's colour and position: the creator's, else the suggestion's, else what was measured, else the editor's choice. The case:
//     the creator's, else what was measured, else the suggestion's, else as typed.
//   - Music: the creator's, else the suggestion's, else "always" when their videos have music, else "always".
//   - Pace: the creator's, else the suggestion's, else like their videos.
func Resolve(p *Profile, suggested *Settings, mine Settings, runVibe string) Resolved {
	var sug Settings
	if suggested != nil {
		sug = *suggested
	}
	var r Resolved
	validVibe := func(v string) string {
		if ValidVibe(v) {
			return v
		}
		return ""
	}
	choose(&r.Vibe, &r.Sources.Vibe, cand{validVibe(runVibe), SourceThisVideo}, cand{validVibe(mine.Vibe), SourceYou},
		cand{validVibe(sug.Vibe), SourceSuggested}, cand{DefaultVibe, SourceDefault})
	chosenVibe := r.Sources.Vibe == SourceThisVideo || r.Sources.Vibe == SourceYou
	vd := vibeDefaults[r.Vibe]

	// Transitions: only the creator's own, or what their videos measurably use (the suggestion follows the measure).
	measured := ""
	if p != nil {
		measured = p.Transitions.Level()
	}
	switch {
	case mine.Transitions != "":
		r.Transitions, r.Sources.Transitions = mine.Transitions, SourceYou
	case measured != "" && sug.Transitions != "" && sug.Transitions != LevelOff:
		r.Transitions, r.Sources.Transitions = sug.Transitions, SourceSuggested
	case measured != "":
		r.Transitions, r.Sources.Transitions = measured, SourceMeasured
	default:
		r.Transitions, r.Sources.Transitions = LevelOff, SourceDefault
	}

	// What else moves.
	for _, m := range []struct {
		dst, src            *string
		mine, sug, fromVibe string
	}{
		{&r.TextAnimation, &r.Sources.TextAnimation, mine.TextAnimation, sug.TextAnimation, vd.TextAnimation},
		{&r.CameraMotion, &r.Sources.CameraMotion, mine.CameraMotion, sug.CameraMotion, vd.CameraMotion},
	} {
		switch {
		case m.mine != "":
			*m.dst, *m.src = m.mine, SourceYou
		case chosenVibe:
			*m.dst, *m.src = m.fromVibe, SourceVibe
		case m.sug != "" && (m.sug != LevelOff || !Energetic(r.Vibe)):
			*m.dst, *m.src = m.sug, SourceSuggested
		default:
			*m.dst, *m.src = m.fromVibe, SourceVibe
		}
	}

	// The title.
	var mColour, mPosition, mCase string
	if p != nil && p.Text != nil && p.Text.TitleShare > 0 {
		mColour = p.Text.Colour
		if p.Text.Position == "upper_third" {
			mPosition = PositionTop
		}
		switch p.Text.Case {
		case "upper":
			mCase = CaseUpper
		case "lower":
			mCase = CaseLower
		case "mixed":
			mCase = CaseAsTyped
		}
	}
	choose(&r.TitleColour, &r.Sources.TitleColour, cand{mine.TitleColour, SourceYou}, cand{sug.TitleColour, SourceSuggested}, cand{mColour, SourceMeasured})
	choose(&r.TitlePosition, &r.Sources.TitlePosition, cand{mine.TitlePosition, SourceYou}, cand{sug.TitlePosition, SourceSuggested},
		cand{mPosition, SourceMeasured}, cand{PositionAuto, SourceDefault})
	// The case is read from the titles themselves: what was measured comes before a suggestion (a summary call that answered as_typed
	// for titles all in capitals made every title mixed case).
	choose(&r.TitleCase, &r.Sources.TitleCase, cand{mine.TitleCase, SourceYou}, cand{mCase, SourceMeasured}, cand{sug.TitleCase, SourceSuggested},
		cand{CaseAsTyped, SourceDefault})

	// Music and pace.
	mMusic := ""
	if p != nil && p.Audio.MusicShare >= 0.6 {
		mMusic = MusicAlways
	}
	choose(&r.Music, &r.Sources.Music, cand{mine.Music, SourceYou}, cand{sug.Music, SourceSuggested}, cand{mMusic, SourceMeasured}, cand{MusicAlways, SourceDefault})
	choose(&r.Pace, &r.Sources.Pace, cand{mine.Pace, SourceYou}, cand{sug.Pace, SourceSuggested}, cand{PaceLikeMine, SourceDefault})

	// What the measures learnt of the titles, the ending and the cuts (P6-05c).
	var mChapters, mTemplate, mHold, mTiming, mAngles string
	if p != nil {
		if t := p.Text; t != nil {
			if t.ChapterRead > 0 {
				mChapters = map[bool]string{true: LevelOn, false: LevelOff}[t.NumberedChapters()]
			}
			if len(t.Templates) > 0 && t.Templates[0].Share >= PatternMinShare && strings.Contains(t.Templates[0].Template, "<") {
				mTemplate = t.Templates[0].Template
			}
		}
		if p.Pacing.EndingS.Median > 0 {
			mHold = map[bool]string{true: HoldLong, false: HoldShort}[p.Pacing.LongEndingHold()]
		}
		if p.Movement.Known() {
			mTiming = map[bool]string{true: CutsMidRep, false: CutsOnFullReps}[p.Movement.MidRepShare >= 0.5]
			mAngles = map[bool]string{true: LevelOn, false: LevelOff}[p.Movement.InsideSetShare >= 0.25]
		}
	}
	choose(&r.ChapterTitles, &r.Sources.ChapterTitles, cand{mine.ChapterTitles, SourceYou}, cand{sug.ChapterTitles, SourceSuggested}, cand{mChapters, SourceMeasured}, cand{LevelOn, SourceDefault})
	choose(&r.TitleTemplate, &r.Sources.TitleTemplate, cand{mine.TitleTemplate, SourceYou}, cand{mTemplate, SourceMeasured})
	choose(&r.EndingHold, &r.Sources.EndingHold, cand{mine.EndingHold, SourceYou}, cand{sug.EndingHold, SourceSuggested}, cand{mHold, SourceMeasured}, cand{HoldLong, SourceDefault})
	choose(&r.CutTiming, &r.Sources.CutTiming, cand{mine.CutTiming, SourceYou}, cand{sug.CutTiming, SourceSuggested}, cand{mTiming, SourceMeasured}, cand{CutsOnFullReps, SourceDefault})
	choose(&r.AngleSwitches, &r.Sources.AngleSwitches, cand{mine.AngleSwitches, SourceYou}, cand{sug.AngleSwitches, SourceSuggested}, cand{mAngles, SourceMeasured}, cand{LevelOn, SourceDefault})
	return r
}

// MeasuredTitleCase is the title_case setting the profile's titles stand for ("" when no case was measured).
func MeasuredTitleCase(p *Profile) string {
	if p == nil || p.Text == nil || p.Text.TitleShare == 0 {
		return ""
	}
	return map[string]string{"upper": CaseUpper, "lower": CaseLower, "mixed": CaseAsTyped}[p.Text.Case]
}

// cand is a candidate value of a setting and where it comes from.
type cand struct{ value, source string }

// choose sets the setting to the first candidate that has a value.
func choose(dst, src *string, cs ...cand) {
	for _, c := range cs {
		if c.value != "" {
			*dst, *src = c.value, c.source
			return
		}
	}
}

// Style is everything the editor knows of the creator's style: the measured profile (nil when no video was measured), the AI summary
// (nil for none) and the effective settings for this edit.
type Style struct {
	Profile  *Profile
	Summary  *Summary
	Mine     Settings
	Resolved Resolved
}

// Empty says there is nothing to follow: no profile and no setting of the creator's.
func (s *Style) Empty() bool {
	return s == nil || (s.Profile == nil && s.Mine.IsZero() && s.Summary == nil)
}

// ForEdit is the style with the vibe of one edit ("" keeps the style's own).
func (s *Style) ForEdit(runVibe string) *Style {
	if s == nil {
		return nil
	}
	out := *s
	var sug *Settings
	if s.Summary != nil {
		sug = &s.Summary.Suggested
	}
	out.Resolved = Resolve(s.Profile, sug, s.Mine, runVibe)
	return &out
}

// Applied is the profile with the creator's title settings put on it, so that the title recipe and the checks follow the creator's
// choice of colour and case over the measured ones (nil without a profile). The profile itself is not changed.
func (s *Style) Applied() *Profile {
	if s == nil || s.Profile == nil {
		return nil
	}
	p := *s.Profile
	if p.Text == nil || p.Text.TitleShare == 0 {
		return &p
	}
	t := *p.Text
	r := s.Resolved
	if r.TitleColour != "" && r.Sources.TitleColour != SourceMeasured {
		t.Colour = r.TitleColour
	}
	switch {
	case r.Sources.TitleCase == SourceMeasured:
	case r.TitleCase == CaseUpper:
		t.Case = "upper"
	case r.TitleCase == CaseLower:
		t.Case = "lower"
	case r.TitleCase == CaseAsTyped:
		t.Case = "mixed"
	}
	if r.TitlePosition == PositionTop || r.TitlePosition == PositionUpperThird {
		t.Position = "upper_third"
	}
	if r.TitleTemplate != "" && r.Sources.TitleTemplate == SourceYou {
		// The creator's own form is the one title template: it goes first, and the measured kinds of title (whose "most common template"
		// would say something else) give way to it. The reference titles stay, for title_copied.
		t.Titles = s.Profile.ReferenceTitles()
		t.Templates = append([]TitleTemplate{{Template: r.TitleTemplate, Share: 1}}, t.Templates...)
		t.Patterns = nil
	}
	p.Text = &t
	return &p
}
