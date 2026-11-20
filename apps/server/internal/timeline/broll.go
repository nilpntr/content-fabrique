package timeline

// The b-roll track (docs/design/broll-cutaways.md): full-frame cutaways from other footage, shown over the main track while its sound
// keeps playing. It is the first change to timeline v1's "exactly one video track": the main track stays the only track of type video
// (so that rule and the schema constant hold), and the cutaways are a track of their own type, which is additive inside v1. A spec
// without the track is exactly what it was: every rule of broll_check.go fires only when the track exists.

// TrackBroll is the type of the b-roll track.
const TrackBroll TrackType = "broll"

// BrollTrackID is the id of the one b-roll track.
const BrollTrackID = "broll"

// MaxCutaways is the most cutaways a timeline may hold (too_many_cutaways): each is an ffmpeg input and about 400 bytes of the filter
// graph, well under MaxFilterGraphBytes at this count.
const MaxCutaways = 12

// BrollTrack holds the cutaways.
type BrollTrack struct {
	ID    string      `json:"id"`
	Items []BrollItem `json:"items"`
}

// TrackID implements Track.
func (t *BrollTrack) TrackID() string { return t.ID }

// TrackType implements Track.
func (*BrollTrack) TrackType() TrackType { return TrackBroll }

// MarshalJSON writes the track with its "type".
func (t BrollTrack) MarshalJSON() ([]byte, error) {
	type plain BrollTrack
	if t.Items == nil {
		t.Items = []BrollItem{}
	}
	return withTag("type", string(TrackBroll), plain(t))
}

// BrollItem is one cutaway: a part of a video asset shown full frame from AtMs for as long as its source range plays, at speed 1. It
// cuts in and out and has no sound of its own (the schema refuses audio, speed, speed_ramp, motion and transition_in on it).
type BrollItem struct {
	ID      string   `json:"id"`
	AssetID string   `json:"asset_id"`
	Source  Source   `json:"source"`
	AtMs    int64    `json:"at_ms"`
	Reframe *Reframe `json:"reframe,omitempty"`
	Look    *Look    `json:"look,omitempty"`
	// Why is the editor's one-line reason for the cutaway, never rendered. A pointer, so the empty string survives a round trip.
	Why *string `json:"why,omitempty"`
}

// DurationMs is how long the cutaway is on screen: its source span (it plays at speed 1). It is zero or negative for a source range
// with in_ms not below out_ms, which the validator refuses.
func (b BrollItem) DurationMs() int64 { return b.Source.OutMs - b.Source.InMs }

// EndMs is where the cutaway leaves the screen, in ms of the output.
func (b BrollItem) EndMs() int64 { return b.AtMs + b.DurationMs() }

// AsVideoItem is the cutaway as a main-track clip with the same picture: its asset, source range, reframe and look, at its time and at
// speed 1, with no motion, transition or sound. The renderer draws a cutaway with the clips' own chain, from this.
func (b BrollItem) AsVideoItem() VideoItem {
	return VideoItem{ID: b.ID, AssetID: b.AssetID, Source: b.Source, AtMs: b.AtMs, Reframe: b.Reframe, Look: b.Look}
}

// BrollTrackOf returns the spec's b-roll track and its index in Tracks, or false when the spec has none.
func BrollTrackOf(spec *Spec) (track *BrollTrack, index int, ok bool) {
	if spec == nil {
		return nil, 0, false
	}
	for i, t := range spec.Tracks {
		if b, isBroll := t.(*BrollTrack); isBroll {
			return b, i, true
		}
	}
	return nil, 0, false
}

// Cutaways returns the items of the spec's b-roll track, nil when it has none.
func Cutaways(spec *Spec) []BrollItem {
	if b, _, ok := BrollTrackOf(spec); ok {
		return b.Items
	}
	return nil
}
