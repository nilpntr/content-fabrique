// Package ops is the edit-op engine of the timeline (docs/design/timeline-v1.md section 4): the typed ops, their strict
// decoding against packages/schemas/timeline/ops.v1.schema.json, Apply (pure and atomic: a batch of ops either gives a
// valid new spec or leaves the old one untouched and says what is wrong, op by op), and the immutable versions built
// on top of it.
//
// The editor agent changes a timeline only through these ops. The rules Apply keeps, in short:
//
//   - The whole batch runs on a copy. The result is validated with timeline.Validate; if it has an error, the original
//     spec is returned unchanged with the issues, each naming the op that caused it when that is known.
//   - The main track is rippled: after an op that changes the length or the order of clips, at_ms of every clip is
//     recomputed so the track stays contiguous. Text and audio keep their absolute times.
//   - New items get ids deterministically ("c<n>", "t<n>", "s<n>": one more than the highest number in use).
//   - The first op that cannot be applied stops the batch; the ops after it are not looked at.
package ops

import (
	"encoding/json"
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Op names, the values of the "op" field.
const (
	NameAddClip         = "add_clip"
	NameRemoveItem      = "remove_item"
	NameMoveItem        = "move_item"
	NameTrimItem        = "trim_item"
	NameReplaceSource   = "replace_source"
	NameSetSpeed        = "set_speed"
	NameSetReframe      = "set_reframe"
	NameAddText         = "add_text"
	NameUpdateText      = "update_text"
	NameRemoveText      = "remove_text"
	NameSetTransition   = "set_transition"
	NameSetSound        = "set_sound"
	NameRemoveSound     = "remove_sound"
	NameSnapCutsToBeats = "snap_cuts_to_beats"
	NameSetLook         = "set_look"
	NameSetCaptions     = "set_captions"
	NameSetClipAudio    = "set_clip_audio"
	NameSetMotion       = "set_motion"
	NameSetSpeedRamp    = "set_speed_ramp"
	NameAddSFX          = "add_sfx"
)

// Names lists the ops in the order of the schema.
var Names = []string{
	NameAddClip, NameRemoveItem, NameMoveItem, NameTrimItem, NameReplaceSource, NameSetSpeed, NameSetReframe,
	NameAddText, NameUpdateText, NameRemoveText, NameSetTransition, NameSetSound, NameRemoveSound,
	NameSnapCutsToBeats, NameSetLook, NameSetCaptions, NameSetClipAudio, NameSetMotion, NameSetSpeedRamp, NameAddSFX,
}

// MaxBatchOps is the most ops in one batch.
const MaxBatchOps = 100

// Op is one edit op. The implementations are the pointer types below (*AddClip, ...), as with timeline.Track; each
// writes its own "op" field when encoded.
type Op interface {
	// OpName is the op's discriminator, the value of its "op" field.
	OpName() string
}

// AddClip adds a clip to the main track.
type AddClip struct {
	// ID is optional; without it the next free "c<n>" is used.
	ID      string          `json:"id,omitempty"`
	AssetID string          `json:"asset_id"`
	Source  timeline.Source `json:"source"`
	// Index is the position the clip will have (0 is the start); AfterID puts it right after that clip. At most one of
	// them; without either the clip goes at the end.
	Index        *int                  `json:"index,omitempty"`
	AfterID      string                `json:"after_id,omitempty"`
	Speed        *float64              `json:"speed,omitempty"`
	SpeedRamp    []timeline.SpeedPoint `json:"speed_ramp,omitempty"`
	Reframe      *timeline.Reframe     `json:"reframe,omitempty"`
	Motion       *timeline.Motion      `json:"motion,omitempty"`
	Look         *timeline.Look        `json:"look,omitempty"`
	ExposureEv   *float64              `json:"exposure_ev,omitempty"`
	TransitionIn *timeline.Transition  `json:"transition_in,omitempty"`
	Audio        *timeline.VideoAudio  `json:"audio,omitempty"`
	MomentID     string                `json:"moment_id,omitempty"`
	Why          *string               `json:"why,omitempty"`
}

// RemoveItem removes a clip from the main track.
type RemoveItem struct {
	ID string `json:"id"`
}

// MoveItem moves a clip (Index or AfterID) or a text item or sound (AtMs). Exactly one of the three is set.
type MoveItem struct {
	ID      string `json:"id"`
	AtMs    *int64 `json:"at_ms,omitempty"`
	Index   *int   `json:"index,omitempty"`
	AfterID string `json:"after_id,omitempty"`
}

// TrimItem trims a clip (InMs, OutMs: the new source range) or a text item or sound (DurationMs).
type TrimItem struct {
	ID         string `json:"id"`
	InMs       *int64 `json:"in_ms,omitempty"`
	OutMs      *int64 `json:"out_ms,omitempty"`
	DurationMs *int64 `json:"duration_ms,omitempty"`
}

// ReplaceSource swaps the asset and source range of a clip. MomentID replaces the clip's moment; without it the
// moment is removed.
type ReplaceSource struct {
	ID       string          `json:"id"`
	AssetID  string          `json:"asset_id"`
	Source   timeline.Source `json:"source"`
	MomentID string          `json:"moment_id,omitempty"`
}

// SetSpeed sets the speed of a clip.
type SetSpeed struct {
	ID    string  `json:"id"`
	Speed float64 `json:"speed"`
}

// SetReframe replaces the reframe of a clip.
type SetReframe struct {
	ID    string          `json:"id"`
	Mode  string          `json:"mode"`
	Focus *timeline.Point `json:"focus,omitempty"`
	Zoom  *float64        `json:"zoom,omitempty"`
}

// AddText adds a text item. Exactly one of Anchor and Position is set.
type AddText struct {
	ID         string          `json:"id,omitempty"`
	AtMs       int64           `json:"at_ms"`
	DurationMs int64           `json:"duration_ms"`
	Text       string          `json:"text"`
	Style      string          `json:"style"`
	Anchor     string          `json:"anchor,omitempty"`
	Position   *timeline.Point `json:"position,omitempty"`
	Animation  string          `json:"animation,omitempty"`
	Fill       string          `json:"fill,omitempty"`
	Scale      *float64        `json:"scale,omitempty"`
	Shadow     string          `json:"shadow,omitempty"`
	Align      string          `json:"align,omitempty"`
}

// UpdateText changes the fields of a text item that are set.
type UpdateText struct {
	ID         string          `json:"id"`
	AtMs       *int64          `json:"at_ms,omitempty"`
	DurationMs *int64          `json:"duration_ms,omitempty"`
	Text       *string         `json:"text,omitempty"`
	Style      string          `json:"style,omitempty"`
	Anchor     string          `json:"anchor,omitempty"`
	Position   *timeline.Point `json:"position,omitempty"`
	Animation  string          `json:"animation,omitempty"`
	Fill       string          `json:"fill,omitempty"`
	Scale      *float64        `json:"scale,omitempty"`
	Shadow     string          `json:"shadow,omitempty"`
	Align      string          `json:"align,omitempty"`
}

// RemoveText removes a text item.
type RemoveText struct {
	ID string `json:"id"`
}

// SetTransition sets how a clip begins. DurationMs is required for everything but a cut.
type SetTransition struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// SetSound adds a sound, or replaces the sound with the same ID. Kind picks the fields: trend_sound has TrendID,
// licensed_track has AssetID and LicenseID, creator_audio has AssetID. The item's export is set from the kind.
type SetSound struct {
	ID             string             `json:"id,omitempty"`
	Kind           timeline.AudioKind `json:"kind"`
	TrendID        string             `json:"trend_id,omitempty"`
	AssetID        string             `json:"asset_id,omitempty"`
	LicenseID      string             `json:"license_id,omitempty"`
	AtMs           *int64             `json:"at_ms,omitempty"`
	SourceOffsetMs *int64             `json:"source_offset_ms,omitempty"`
	DurationMs     *int64             `json:"duration_ms,omitempty"`
	GainDB         *float64           `json:"gain_db,omitempty"`
	FadeInMs       *int64             `json:"fade_in_ms,omitempty"`
	FadeOutMs      *int64             `json:"fade_out_ms,omitempty"`
}

// RemoveSound removes a sound from the audio track.
type RemoveSound struct {
	ID string `json:"id"`
}

// BeatGrid describes beats: a constant tempo (BPM and FirstBeatMs) or an explicit list (BeatsMs).
type BeatGrid struct {
	BPM            *float64  `json:"bpm,omitempty"`
	FirstBeatMs    *float64  `json:"first_beat_ms,omitempty"`
	BeatsPerBar    *int      `json:"beats_per_bar,omitempty"`
	DownbeatOffset *int      `json:"downbeat_offset,omitempty"`
	BeatsMs        []float64 `json:"beats_ms,omitempty"`
	DownbeatsMs    []float64 `json:"downbeats_ms,omitempty"`
}

// SnapOptions are the options of SnapCutsToBeats.
type SnapOptions struct {
	MaxShiftMs      *int64   `json:"max_shift_ms,omitempty"`
	MaxShiftBeats   *float64 `json:"max_shift_beats,omitempty"`
	PreferDownbeats bool     `json:"prefer_downbeats,omitempty"`
	MinShotMs       *int64   `json:"min_shot_ms,omitempty"`
	WriteMarkers    bool     `json:"write_markers,omitempty"`
}

// SnapCutsToBeats moves the cuts of the main track onto the beats of Grid. With SoundID the grid is in that sound's
// time and is placed with the sound's at_ms and source_offset_ms; without it the grid is in timeline time.
type SnapCutsToBeats struct {
	Grid    BeatGrid     `json:"grid"`
	SoundID string       `json:"sound_id,omitempty"`
	Options *SnapOptions `json:"options,omitempty"`
}

// SetLook sets the look of a clip, or of every clip when ID is empty.
type SetLook struct {
	ID     string `json:"id,omitempty"`
	Preset string `json:"preset"`
	// Match is whether the clip's exposure_ev hint is applied; left out, the clip's own setting stays.
	Match *bool `json:"match,omitempty"`
	// ExposureEv sets the exposure hint of the clip ID names.
	ExposureEv *float64 `json:"exposure_ev,omitempty"`
}

// SetCaptions creates or changes the captions track. The fields that are set change; the others stay.
type SetCaptions struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	Style     string `json:"style,omitempty"`
	Uppercase *bool  `json:"uppercase,omitempty"`
	MaxWords  *int   `json:"max_words,omitempty"`
}

// SetClipAudio sets whether a clip's own sound plays and how loud. The fields that are set change.
type SetClipAudio struct {
	// ID is the clip; empty sets every clip of the main track (the way to set the bed policy once).
	ID        string   `json:"id,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	UseSource *bool    `json:"use_source,omitempty"`
	GainDB    *float64 `json:"gain_db,omitempty"`
}

// SetMotion sets the camera move of a clip, replacing its whole motion: fields left out go back to their defaults, and the
// preset none removes it.
type SetMotion struct {
	ID       string   `json:"id"`
	Preset   string   `json:"preset"`
	AtMs     *int64   `json:"at_ms,omitempty"`
	Strength *float64 `json:"strength,omitempty"`
}

// AddSFX adds a generated sound effect to the audio track.
type AddSFX struct {
	// ID is optional; without it the next free "s<n>" is used.
	ID      string   `json:"id,omitempty"`
	Sfx     string   `json:"sfx"`
	Variant string   `json:"variant,omitempty"`
	AtMs    int64    `json:"at_ms"`
	GainDB  *float64 `json:"gain_db,omitempty"`
}

// SetSpeedRamp gives a clip a speed ramp, which replaces its speed.
type SetSpeedRamp struct {
	ID     string                `json:"id"`
	Points []timeline.SpeedPoint `json:"points"`
}

// OpName implements Op.
func (*AddClip) OpName() string { return NameAddClip }

// OpName implements Op.
func (*RemoveItem) OpName() string { return NameRemoveItem }

// OpName implements Op.
func (*MoveItem) OpName() string { return NameMoveItem }

// OpName implements Op.
func (*TrimItem) OpName() string { return NameTrimItem }

// OpName implements Op.
func (*ReplaceSource) OpName() string { return NameReplaceSource }

// OpName implements Op.
func (*SetSpeed) OpName() string { return NameSetSpeed }

// OpName implements Op.
func (*SetReframe) OpName() string { return NameSetReframe }

// OpName implements Op.
func (*AddText) OpName() string { return NameAddText }

// OpName implements Op.
func (*UpdateText) OpName() string { return NameUpdateText }

// OpName implements Op.
func (*RemoveText) OpName() string { return NameRemoveText }

// OpName implements Op.
func (*SetTransition) OpName() string { return NameSetTransition }

// OpName implements Op.
func (*SetSound) OpName() string { return NameSetSound }

// OpName implements Op.
func (*RemoveSound) OpName() string { return NameRemoveSound }

// OpName implements Op.
func (*SnapCutsToBeats) OpName() string { return NameSnapCutsToBeats }

// OpName implements Op.
func (*SetLook) OpName() string { return NameSetLook }

// OpName implements Op.
func (*SetCaptions) OpName() string { return NameSetCaptions }

// OpName implements Op.
func (*SetClipAudio) OpName() string { return NameSetClipAudio }

// OpName implements Op.
func (*SetMotion) OpName() string { return NameSetMotion }

// OpName implements Op.
func (*SetSpeedRamp) OpName() string { return NameSetSpeedRamp }

// OpName implements Op.
func (*AddSFX) OpName() string { return NameAddSFX }

// ---- encoding ---------------------------------------------------------------------------------------------------

// tagged encodes v (a struct without an "op" field) with the op's name first.
func tagged(name string, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf(`{"op":%q`, name)
	if string(body) == "{}" {
		return []byte(head + "}"), nil
	}
	return append([]byte(head+","), body[1:]...), nil
}

// MarshalJSON writes the op with its "op" field.
func (o AddClip) MarshalJSON() ([]byte, error) { type p AddClip; return tagged(NameAddClip, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o RemoveItem) MarshalJSON() ([]byte, error) {
	type p RemoveItem
	return tagged(NameRemoveItem, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o MoveItem) MarshalJSON() ([]byte, error) { type p MoveItem; return tagged(NameMoveItem, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o TrimItem) MarshalJSON() ([]byte, error) { type p TrimItem; return tagged(NameTrimItem, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o ReplaceSource) MarshalJSON() ([]byte, error) {
	type p ReplaceSource
	return tagged(NameReplaceSource, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetSpeed) MarshalJSON() ([]byte, error) { type p SetSpeed; return tagged(NameSetSpeed, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o SetReframe) MarshalJSON() ([]byte, error) {
	type p SetReframe
	return tagged(NameSetReframe, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o AddText) MarshalJSON() ([]byte, error) { type p AddText; return tagged(NameAddText, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o UpdateText) MarshalJSON() ([]byte, error) {
	type p UpdateText
	return tagged(NameUpdateText, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o RemoveText) MarshalJSON() ([]byte, error) {
	type p RemoveText
	return tagged(NameRemoveText, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetTransition) MarshalJSON() ([]byte, error) {
	type p SetTransition
	return tagged(NameSetTransition, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetSound) MarshalJSON() ([]byte, error) { type p SetSound; return tagged(NameSetSound, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o RemoveSound) MarshalJSON() ([]byte, error) {
	type p RemoveSound
	return tagged(NameRemoveSound, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SnapCutsToBeats) MarshalJSON() ([]byte, error) {
	type p SnapCutsToBeats
	return tagged(NameSnapCutsToBeats, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetLook) MarshalJSON() ([]byte, error) { type p SetLook; return tagged(NameSetLook, p(o)) }

// MarshalJSON writes the op with its "op" field.
func (o SetCaptions) MarshalJSON() ([]byte, error) {
	type p SetCaptions
	return tagged(NameSetCaptions, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetClipAudio) MarshalJSON() ([]byte, error) {
	type p SetClipAudio
	return tagged(NameSetClipAudio, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetMotion) MarshalJSON() ([]byte, error) {
	type p SetMotion
	return tagged(NameSetMotion, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o SetSpeedRamp) MarshalJSON() ([]byte, error) {
	type p SetSpeedRamp
	return tagged(NameSetSpeedRamp, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o AddSFX) MarshalJSON() ([]byte, error) { type p AddSFX; return tagged(NameAddSFX, p(o)) }

// MarshalBatch encodes a batch of ops as a JSON array.
func MarshalBatch(batch []Op) ([]byte, error) {
	if batch == nil {
		batch = []Op{}
	}
	return json.Marshal(batch)
}

// newOp returns an empty op of the given name, for decoding.
func newOp(name string) Op {
	switch name {
	case NameAddClip:
		return &AddClip{}
	case NameRemoveItem:
		return &RemoveItem{}
	case NameMoveItem:
		return &MoveItem{}
	case NameTrimItem:
		return &TrimItem{}
	case NameReplaceSource:
		return &ReplaceSource{}
	case NameSetSpeed:
		return &SetSpeed{}
	case NameSetReframe:
		return &SetReframe{}
	case NameAddText:
		return &AddText{}
	case NameUpdateText:
		return &UpdateText{}
	case NameRemoveText:
		return &RemoveText{}
	case NameSetTransition:
		return &SetTransition{}
	case NameSetSound:
		return &SetSound{}
	case NameRemoveSound:
		return &RemoveSound{}
	case NameSnapCutsToBeats:
		return &SnapCutsToBeats{}
	case NameSetLook:
		return &SetLook{}
	case NameSetCaptions:
		return &SetCaptions{}
	case NameSetClipAudio:
		return &SetClipAudio{}
	case NameSetMotion:
		return &SetMotion{}
	case NameSetSpeedRamp:
		return &SetSpeedRamp{}
	case NameAddSFX:
		return &AddSFX{}
	}
	return nil
}
