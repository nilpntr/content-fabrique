package timeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// DecodeError is a decoding problem at a known place in the document.
type DecodeError struct {
	// Path is a JSON Pointer into the document.
	Path    string
	Message string
}

func (e *DecodeError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// Parse decodes a spec strictly: an unknown field anywhere, a key written in another case ("ID" for "id": Go's
// encoding/json would accept it, the schema does not), a whole number written as 3000.0, a wrong type, a track "type"
// or audio "kind" the spec does not define, or trailing data after the document is an error. It does not check
// ranges or semantics; use Validate for that.
func Parse(data []byte) (*Spec, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return parseDoc(doc, data)
}

// parseDoc is Parse for a caller that has already decoded data into a generic document (numbers as json.Number), as
// Validate has for the schema: the shape check runs on that document, so it is not decoded a third time.
func parseDoc(doc any, data []byte) (*Spec, error) {
	if err := shapeCheck(doc); err != nil {
		return nil, err
	}
	var s Spec
	if err := decodeStrict(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// decodeStrict unmarshals one JSON value into v with unknown fields disallowed and no trailing data. The custom
// UnmarshalJSON methods below call it too, because encoding/json does not pass DisallowUnknownFields down to them.
// Key case is checked separately, by shapeCheck, which Parse runs first.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the top-level value")
	}
	return nil
}

// MarshalJSON writes a nil Tracks or Markers as [], because the schema requires both arrays.
func (s Spec) MarshalJSON() ([]byte, error) {
	type plain Spec
	if s.Tracks == nil {
		s.Tracks = []Track{}
	}
	if s.Markers == nil {
		s.Markers = []Marker{}
	}
	return json.Marshal(plain(s))
}

// UnmarshalJSON decodes the tracks by their "type".
func (s *Spec) UnmarshalJSON(data []byte) error {
	type plain Spec // no methods, so no recursion
	aux := struct {
		Tracks []json.RawMessage `json:"tracks"`
		*plain
	}{plain: (*plain)(s)}
	// The embedded Tracks field of plain is shadowed by the shallower aux.Tracks.
	if err := decodeStrict(data, &aux); err != nil {
		return err
	}
	s.Tracks = nil
	if aux.Tracks != nil {
		s.Tracks = make([]Track, 0, len(aux.Tracks))
	}
	for i, raw := range aux.Tracks {
		t, err := decodeTrack(raw)
		if err != nil {
			return fmt.Errorf("tracks[%d]: %w", i, err)
		}
		s.Tracks = append(s.Tracks, t)
	}
	return nil
}

// trackAux has the discriminator and the fields of every track type, so that one strict pass reads any of them. The
// fields of the other types are pointers (or raw), so that a field on the wrong type can be told from an absent one.
type trackAux struct {
	Type      TrackType       `json:"type"`
	ID        string          `json:"id"`
	Items     json.RawMessage `json:"items"`
	From      *string         `json:"from"`
	Style     *string         `json:"style"`
	Enabled   *bool           `json:"enabled"`
	Uppercase *bool           `json:"uppercase"`
	MaxWords  *int            `json:"max_words"`
}

func (a *trackAux) captionsField() string {
	switch {
	case a.From != nil:
		return "from"
	case a.Style != nil:
		return "style"
	case a.Enabled != nil:
		return "enabled"
	case a.Uppercase != nil:
		return "uppercase"
	case a.MaxWords != nil:
		return "max_words"
	}
	return ""
}

func decodeTrack(raw json.RawMessage) (Track, error) {
	var a trackAux
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	if a.Type != TrackCaptions {
		if f := a.captionsField(); f != "" {
			return nil, fmt.Errorf("field %q is only allowed on a captions track, not on a %s track", f, a.Type)
		}
	}
	switch a.Type {
	case TrackVideo:
		t := &VideoTrack{ID: a.ID}
		if err := decodeItems(a.Items, &t.Items); err != nil {
			return nil, err
		}
		return t, nil
	case TrackText:
		t := &TextTrack{ID: a.ID}
		if err := decodeItems(a.Items, &t.Items); err != nil {
			return nil, err
		}
		return t, nil
	case TrackAudio:
		t := &AudioTrack{ID: a.ID}
		items, err := decodeAudioItems(a.Items)
		if err != nil {
			return nil, err
		}
		t.Items = items
		return t, nil
	case TrackCaptions:
		if a.Items != nil {
			return nil, errors.New(`field "items" is not allowed on a captions track`)
		}
		t := &CaptionsTrack{ID: a.ID, Uppercase: a.Uppercase, MaxWords: a.MaxWords}
		if a.From != nil {
			t.From = *a.From
		}
		if a.Style != nil {
			t.Style = *a.Style
		}
		if a.Enabled != nil {
			t.Enabled = *a.Enabled
		}
		return t, nil
	}
	return nil, fmt.Errorf("unknown track type %q", a.Type)
}

func decodeItems[T any](raw json.RawMessage, into *[]T) error {
	if raw == nil {
		return nil
	}
	return decodeStrict(raw, into)
}

// UnmarshalJSON decodes the items by their "kind".
func (t *AudioTrack) UnmarshalJSON(data []byte) error {
	var aux struct {
		ID    string          `json:"id"`
		Items json.RawMessage `json:"items"`
	}
	if err := decodeStrict(data, &aux); err != nil {
		return err
	}
	items, err := decodeAudioItems(aux.Items)
	if err != nil {
		return err
	}
	t.ID, t.Items = aux.ID, items
	return nil
}

func decodeAudioItems(raw json.RawMessage) ([]AudioItem, error) {
	if raw == nil {
		return nil, nil
	}
	var raws []json.RawMessage
	if err := decodeStrict(raw, &raws); err != nil {
		return nil, err
	}
	items := make([]AudioItem, 0, len(raws))
	for i, r := range raws {
		it, err := decodeAudioItem(r)
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		items = append(items, it)
	}
	return items, nil
}

// audioAux has the discriminator and the fields of every audio kind, for one strict pass.
type audioAux struct {
	Kind AudioKind `json:"kind"`
	AudioCommon
	TrendID   *string `json:"trend_id"`
	AssetID   *string `json:"asset_id"`
	LicenseID *string `json:"license_id"`
	Sfx       *string `json:"sfx"`
}

func decodeAudioItem(raw json.RawMessage) (AudioItem, error) {
	var a audioAux
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	notAllowed := func(field string) error {
		return fmt.Errorf("field %q is not allowed on a %s item", field, a.Kind)
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	if a.Kind != AudioSFX && a.Sfx != nil {
		return nil, notAllowed("sfx")
	}
	switch a.Kind {
	case AudioSFX:
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"asset_id", a.AssetID != nil}, {"license_id", a.LicenseID != nil}, {"trend_id", a.TrendID != nil},
			{"duration_ms", a.DurationMs != nil}, {"fade_in_ms", a.FadeInMs != nil}, {"fade_out_ms", a.FadeOutMs != nil},
		} {
			if f.set {
				return nil, notAllowed(f.name)
			}
		}
		return &SFX{ID: a.ID, Sfx: str(a.Sfx), AtMs: a.AtMs, Export: a.Export, GainDB: a.GainDB}, nil
	case AudioTrendSound:
		if a.AssetID != nil {
			return nil, notAllowed("asset_id")
		}
		if a.LicenseID != nil {
			return nil, notAllowed("license_id")
		}
		return &TrendSound{AudioCommon: a.AudioCommon, TrendID: str(a.TrendID)}, nil
	case AudioLicensedTrack:
		if a.TrendID != nil {
			return nil, notAllowed("trend_id")
		}
		return &LicensedTrack{AudioCommon: a.AudioCommon, AssetID: str(a.AssetID), LicenseID: str(a.LicenseID)}, nil
	case AudioCreatorAudio:
		if a.TrendID != nil {
			return nil, notAllowed("trend_id")
		}
		if a.LicenseID != nil {
			return nil, notAllowed("license_id")
		}
		return &CreatorAudio{AudioCommon: a.AudioCommon, AssetID: str(a.AssetID)}, nil
	}
	return nil, fmt.Errorf("unknown audio kind %q", a.Kind)
}

// withTag encodes v with the discriminator field first.
func withTag(field, tag string, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	head, err := json.Marshal(map[string]string{field: tag})
	if err != nil {
		return nil, err
	}
	if string(body) == "{}" {
		return head, nil
	}
	// head is {"type":"x"} and body is {...}: splice them into one object.
	out := make([]byte, 0, len(head)+len(body))
	out = append(out, head[:len(head)-1]...)
	out = append(out, ',')
	out = append(out, body[1:]...)
	return out, nil
}

// MarshalJSON writes the track with its "type".
func (t VideoTrack) MarshalJSON() ([]byte, error) {
	type plain VideoTrack
	if t.Items == nil {
		t.Items = []VideoItem{}
	}
	return withTag("type", string(TrackVideo), plain(t))
}

// MarshalJSON writes the track with its "type".
func (t TextTrack) MarshalJSON() ([]byte, error) {
	type plain TextTrack
	if t.Items == nil {
		t.Items = []TextItem{}
	}
	return withTag("type", string(TrackText), plain(t))
}

// MarshalJSON writes the track with its "type".
func (t CaptionsTrack) MarshalJSON() ([]byte, error) {
	type plain CaptionsTrack
	return withTag("type", string(TrackCaptions), plain(t))
}

// MarshalJSON writes the track with its "type".
func (t AudioTrack) MarshalJSON() ([]byte, error) {
	type plain AudioTrack
	if t.Items == nil {
		t.Items = []AudioItem{}
	}
	return withTag("type", string(TrackAudio), plain(t))
}

// MarshalJSON writes the item with its "kind".
func (a TrendSound) MarshalJSON() ([]byte, error) {
	type plain TrendSound
	return withTag("kind", string(AudioTrendSound), plain(a))
}

// MarshalJSON writes the item with its "kind".
func (a LicensedTrack) MarshalJSON() ([]byte, error) {
	type plain LicensedTrack
	return withTag("kind", string(AudioLicensedTrack), plain(a))
}

// MarshalJSON writes the item with its "kind".
func (a SFX) MarshalJSON() ([]byte, error) {
	type plain SFX
	return withTag("kind", string(AudioSFX), plain(a))
}

// MarshalJSON writes the item with its "kind".
func (a CreatorAudio) MarshalJSON() ([]byte, error) {
	type plain CreatorAudio
	return withTag("kind", string(AudioCreatorAudio), plain(a))
}
