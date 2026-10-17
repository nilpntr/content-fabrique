// Package timeline is the Go side of the timeline spec v1 (docs/design/timeline-v1.md): the types, strict decoding,
// validation against the JSON Schema plus the semantic checks the schema cannot express, and the time arithmetic the
// compiler and the ops share.
//
// The types here are written by hand, because generated Go from JSON Schema is poor for tagged unions. They mirror
// packages/schemas/timeline/v1.schema.json, and a test decodes every valid fixture with unknown fields disallowed,
// encodes it again and compares the two semantically, so the two cannot drift apart.
//
// # Defaults
//
// The spec leaves some fields optional and names a default for each (speed 1, focus 0.5/0.5, zoom 1, ...). The types
// keep an absent field absent: optional fields are pointers (or omitempty strings), so decoding and encoding a spec
// never adds or drops a field, and a spec that has been through Go still diffs cleanly against the one that went in.
// The default is applied on read, through the accessor methods (VideoItem.SpeedOrDefault, Reframe.FocusOrDefault,
// ...), so the compiler and the validator never repeat the defaults. The defaults are the Default* constants below.
package timeline

// SchemaVersion is the constant value of the top-level "schema" field.
const SchemaVersion = "contentfabrique.timeline/v1"

// MainTrackID is the id of the one video track.
const MainTrackID = "main"

// Limits of the spec.
const (
	// MaxTimeMs is the largest value any time field may have (24 hours). Go sums times as int64, so a sum of several
	// fields cannot overflow.
	MaxTimeMs int64 = 86_400_000
	// MinShotMs is the shortest output duration of a main item and of a text item.
	MinShotMs int64 = 300
	// MinTotalMs and MaxTotalMs bound the total duration of a timeline.
	MinTotalMs int64 = 1_000
	MaxTotalMs int64 = 180_000
)

// The defaults of the optional fields. The compiler applies them; the types never write them into a spec.
const (
	DefaultSpeed      = 1.0
	DefaultFocus      = 0.5
	DefaultZoom       = 1.0
	DefaultUppercase  = true
	DefaultMaxWords   = 2
	DefaultStrength   = 1.0
	DefaultGainDB     = 0.0
	DefaultUseSource  = false
	DefaultReframe    = ReframeCover
	DefaultLook       = LookNone
	DefaultAnimation  = AnimationNone
	DefaultTransition = TransitionCut
)

// Format is an output format (D-25).
type Format string

// The four output formats.
const (
	Format9x16 Format = "9:16"
	Format1x1  Format = "1:1"
	Format4x5  Format = "4:5"
	Format16x9 Format = "16:9"
)

// Size returns the width and height the format requires, and false for an unknown format.
func (f Format) Size() (width, height int, ok bool) {
	switch f {
	case Format9x16:
		return 1080, 1920, true
	case Format1x1:
		return 1080, 1080, true
	case Format4x5:
		return 1080, 1350, true
	case Format16x9:
		return 1920, 1080, true
	}
	return 0, 0, false
}

// The enum values of the spec that the Go code switches on.
const (
	ReframeCover    = "cover"
	ReframeContain  = "contain"
	ReframeBlurFill = "blur_fill"

	LookNone = "none"

	AnimationNone = "none"

	TransitionCut        = "cut"
	TransitionCrossfade  = "crossfade"
	TransitionDipToBlack = "dip_to_black"
	TransitionWhip       = "whip"

	AnchorUpperThird = "upper_third"
	AnchorCenter     = "center"
	AnchorLowerThird = "lower_third"

	ExportCueOnly  = "cue_only"
	ExportLicensed = "licensed"

	MarkerBeat     = "beat"
	MarkerDownbeat = "downbeat"
	MarkerDrop     = "drop"
	MarkerSection  = "section"
)

// Spec is a timeline: the top-level document.
type Spec struct {
	Schema  string   `json:"schema"`
	Output  Output   `json:"output"`
	Tracks  []Track  `json:"tracks"`
	Markers []Marker `json:"markers"`
	Meta    *Meta    `json:"meta,omitempty"`
}

// Output describes the rendered video.
type Output struct {
	Format     Format `json:"format"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPS        int    `json:"fps"`
	Background string `json:"background"`
}

// Meta links a timeline to the idea and concept it came from.
type Meta struct {
	IdeaID    string `json:"idea_id,omitempty"`
	ConceptID string `json:"concept_id,omitempty"`
}

// Marker is a point in time the editor can snap to (a beat, a drop, a section start).
type Marker struct {
	AtMs     int64    `json:"at_ms"`
	Kind     string   `json:"kind"`
	Strength *float64 `json:"strength,omitempty"`
}

// StrengthOrDefault is the marker's strength, or 1 when the spec leaves it out.
func (m Marker) StrengthOrDefault() float64 {
	if m.Strength == nil {
		return DefaultStrength
	}
	return *m.Strength
}

// TrackType is the discriminator of a Track.
type TrackType string

// The four track types.
const (
	TrackVideo    TrackType = "video"
	TrackText     TrackType = "text"
	TrackCaptions TrackType = "captions"
	TrackAudio    TrackType = "audio"
)

// Track is one of VideoTrack, TextTrack, CaptionsTrack and AudioTrack (all value types). The "type" field tells them
// apart in JSON; Spec's decoder picks the Go type from it, and each type writes its own "type" when encoded.
type Track interface {
	// TrackID is the track's id.
	TrackID() string
	// TrackType is the track's discriminator.
	TrackType() TrackType
}

// VideoTrack is the main track: the cut, in order.
type VideoTrack struct {
	ID    string      `json:"id"`
	Items []VideoItem `json:"items"`
}

// TextTrack holds the text overlays.
type TextTrack struct {
	ID    string     `json:"id"`
	Items []TextItem `json:"items"`
}

// CaptionsTrack turns the transcript into burned-in captions. It has no items.
type CaptionsTrack struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Style     string `json:"style"`
	Enabled   bool   `json:"enabled"`
	Uppercase *bool  `json:"uppercase,omitempty"`
	MaxWords  *int   `json:"max_words,omitempty"`
}

// AudioTrack holds the music and sound items.
type AudioTrack struct {
	ID    string      `json:"id"`
	Items []AudioItem `json:"items"`
}

// TrackID implements Track.
func (t VideoTrack) TrackID() string { return t.ID }

// TrackID implements Track.
func (t TextTrack) TrackID() string { return t.ID }

// TrackID implements Track.
func (t CaptionsTrack) TrackID() string { return t.ID }

// TrackID implements Track.
func (t AudioTrack) TrackID() string { return t.ID }

// TrackType implements Track.
func (VideoTrack) TrackType() TrackType { return TrackVideo }

// TrackType implements Track.
func (TextTrack) TrackType() TrackType { return TrackText }

// TrackType implements Track.
func (CaptionsTrack) TrackType() TrackType { return TrackCaptions }

// TrackType implements Track.
func (AudioTrack) TrackType() TrackType { return TrackAudio }

// UppercaseOrDefault is the captions' uppercase flag, or true when the spec leaves it out.
func (t CaptionsTrack) UppercaseOrDefault() bool {
	if t.Uppercase == nil {
		return DefaultUppercase
	}
	return *t.Uppercase
}

// MaxWordsOrDefault is the captions' max_words, or 2 when the spec leaves it out.
func (t CaptionsTrack) MaxWordsOrDefault() int {
	if t.MaxWords == nil {
		return DefaultMaxWords
	}
	return *t.MaxWords
}

// VideoItem is one clip on the main track.
type VideoItem struct {
	ID           string      `json:"id"`
	AssetID      string      `json:"asset_id"`
	MomentID     string      `json:"moment_id,omitempty"`
	Source       Source      `json:"source"`
	AtMs         int64       `json:"at_ms"`
	Speed        *float64    `json:"speed,omitempty"`
	Reframe      *Reframe    `json:"reframe,omitempty"`
	Look         *Look       `json:"look,omitempty"`
	TransitionIn *Transition `json:"transition_in,omitempty"`
	Audio        *VideoAudio `json:"audio,omitempty"`
	// Why is the agent's one-line reason for the clip. It is never rendered. A pointer, because the empty string is
	// valid and must survive a round trip.
	Why *string `json:"why,omitempty"`
}

// Source is the range of the asset a clip plays, in the asset's own time.
type Source struct {
	InMs  int64 `json:"in_ms"`
	OutMs int64 `json:"out_ms"`
}

// SpeedOrDefault is the clip's speed, or 1 when the spec leaves it out.
func (v VideoItem) SpeedOrDefault() float64 {
	if v.Speed == nil {
		return DefaultSpeed
	}
	return *v.Speed
}

// TransitionDurationMs is the length of the transition into the clip: 0 for a cut or no transition.
func (v VideoItem) TransitionDurationMs() int64 {
	if v.TransitionIn == nil || v.TransitionIn.Type == TransitionCut {
		return 0
	}
	return v.TransitionIn.DurationMs
}

// Point is a position or focus point, both coordinates in [0, 1].
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Reframe says how a clip is fitted into the output frame.
type Reframe struct {
	Mode  string   `json:"mode"`
	Focus *Point   `json:"focus,omitempty"`
	Zoom  *float64 `json:"zoom,omitempty"`
}

// FocusOrDefault is the reframe focus, or the centre (0.5, 0.5) when the spec leaves it out.
func (r Reframe) FocusOrDefault() Point {
	if r.Focus == nil {
		return Point{X: DefaultFocus, Y: DefaultFocus}
	}
	return *r.Focus
}

// ZoomOrDefault is the reframe zoom, or 1 when the spec leaves it out.
func (r Reframe) ZoomOrDefault() float64 {
	if r.Zoom == nil {
		return DefaultZoom
	}
	return *r.Zoom
}

// Look is a colour preset.
type Look struct {
	Preset string `json:"preset"`
}

// Transition is how a clip comes in: a cut, or a crossfade, dip_to_black or whip of DurationMs (100 to 1000). A cut
// has no duration.
type Transition struct {
	Type       string `json:"type"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// VideoAudio controls the source audio of a clip.
type VideoAudio struct {
	UseSource *bool    `json:"use_source,omitempty"`
	GainDB    *float64 `json:"gain_db,omitempty"`
}

// UseSourceOrDefault is whether the clip's own audio plays: false when the spec leaves it out.
func (a VideoAudio) UseSourceOrDefault() bool {
	if a.UseSource == nil {
		return DefaultUseSource
	}
	return *a.UseSource
}

// GainDBOrDefault is the source audio gain in dB, or 0 when the spec leaves it out.
func (a VideoAudio) GainDBOrDefault() float64 {
	if a.GainDB == nil {
		return DefaultGainDB
	}
	return *a.GainDB
}

// TextItem is one text overlay. Exactly one of Anchor and Position is set.
type TextItem struct {
	ID         string `json:"id"`
	AtMs       int64  `json:"at_ms"`
	DurationMs int64  `json:"duration_ms"`
	Text       string `json:"text"`
	Style      string `json:"style"`
	Anchor     string `json:"anchor,omitempty"`
	Position   *Point `json:"position,omitempty"`
	Animation  string `json:"animation,omitempty"`
}

// AnimationOrDefault is the text animation, or "none" when the spec leaves it out.
func (t TextItem) AnimationOrDefault() string {
	if t.Animation == "" {
		return DefaultAnimation
	}
	return t.Animation
}

// AudioKind is the discriminator of an AudioItem.
type AudioKind string

// The three audio kinds.
const (
	AudioTrendSound    AudioKind = "trend_sound"
	AudioLicensedTrack AudioKind = "licensed_track"
	AudioCreatorAudio  AudioKind = "creator_audio"
)

// AudioItem is one of TrendSound, LicensedTrack and CreatorAudio (all value types). The "kind" field tells them
// apart in JSON; each type writes its own "kind" when encoded.
type AudioItem interface {
	// AudioKind is the item's discriminator.
	AudioKind() AudioKind
	// Common returns the fields every kind has.
	Common() AudioCommon
	// AssetRef is the platform asset the item plays, and false for a trend sound, which has none.
	AssetRef() (string, bool)
}

// AudioCommon holds the fields all audio kinds share.
type AudioCommon struct {
	ID             string   `json:"id"`
	AtMs           int64    `json:"at_ms"`
	SourceOffsetMs int64    `json:"source_offset_ms"`
	Export         string   `json:"export"`
	DurationMs     *int64   `json:"duration_ms,omitempty"`
	GainDB         *float64 `json:"gain_db,omitempty"`
	FadeInMs       *int64   `json:"fade_in_ms,omitempty"`
	FadeOutMs      *int64   `json:"fade_out_ms,omitempty"`
}

// Common returns the item's shared fields.
func (c AudioCommon) Common() AudioCommon { return c }

// GainDBOrDefault is the gain in dB, or 0 when the spec leaves it out.
func (c AudioCommon) GainDBOrDefault() float64 {
	if c.GainDB == nil {
		return DefaultGainDB
	}
	return *c.GainDB
}

// FadeMs returns the fade in and fade out lengths, 0 where the spec leaves one out.
func (c AudioCommon) FadeMs() (in, out int64) {
	if c.FadeInMs != nil {
		in = *c.FadeInMs
	}
	if c.FadeOutMs != nil {
		out = *c.FadeOutMs
	}
	return in, out
}

// EffectiveDurationMs is how long the item plays on a timeline of totalMs: its duration_ms, or, when the spec leaves
// that out, everything from at_ms to the end of the timeline. It is negative or zero when the item starts at or past
// the end.
func (c AudioCommon) EffectiveDurationMs(totalMs int64) int64 {
	if c.DurationMs != nil {
		return *c.DurationMs
	}
	return totalMs - c.AtMs
}

// TrendSound is a trending sound that is only referenced: it is never mixed into the export (D-03).
type TrendSound struct {
	AudioCommon
	TrendID string `json:"trend_id"`
}

// LicensedTrack is a music track with a licence record.
type LicensedTrack struct {
	AudioCommon
	AssetID   string `json:"asset_id"`
	LicenseID string `json:"license_id"`
}

// CreatorAudio is the creator's own upload, with a rights attestation.
type CreatorAudio struct {
	AudioCommon
	AssetID string `json:"asset_id"`
}

// AudioKind implements AudioItem.
func (TrendSound) AudioKind() AudioKind { return AudioTrendSound }

// AudioKind implements AudioItem.
func (LicensedTrack) AudioKind() AudioKind { return AudioLicensedTrack }

// AudioKind implements AudioItem.
func (CreatorAudio) AudioKind() AudioKind { return AudioCreatorAudio }

// AssetRef implements AudioItem.
func (TrendSound) AssetRef() (string, bool) { return "", false }

// AssetRef implements AudioItem.
func (l LicensedTrack) AssetRef() (string, bool) { return l.AssetID, true }

// AssetRef implements AudioItem.
func (c CreatorAudio) AssetRef() (string, bool) { return c.AssetID, true }
