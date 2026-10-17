package timeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Parse decodes a spec strictly: an unknown field anywhere, a wrong type, a track "type" or audio "kind" the spec
// does not define, or trailing data after the document is an error. It does not check ranges or semantics; use
// Validate for that (Validate calls Parse after the schema has passed).
func Parse(data []byte) (*Spec, error) {
	var s Spec
	if err := decodeStrict(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// decodeStrict unmarshals one JSON value into v with unknown fields disallowed and no trailing data. The custom
// UnmarshalJSON methods below call it too, because encoding/json does not pass DisallowUnknownFields down to them.
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

func decodeTrack(raw json.RawMessage) (Track, error) {
	var d struct {
		Type TrackType `json:"type"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	switch d.Type {
	case TrackVideo:
		var t VideoTrack
		if err := decodeTagged(raw, "type", &t); err != nil {
			return nil, err
		}
		return t, nil
	case TrackText:
		var t TextTrack
		if err := decodeTagged(raw, "type", &t); err != nil {
			return nil, err
		}
		return t, nil
	case TrackCaptions:
		var t CaptionsTrack
		if err := decodeTagged(raw, "type", &t); err != nil {
			return nil, err
		}
		return t, nil
	case TrackAudio:
		var t AudioTrack
		if err := decodeTagged(raw, "type", &t); err != nil {
			return nil, err
		}
		return t, nil
	}
	return nil, fmt.Errorf("unknown track type %q", d.Type)
}

// decodeTagged decodes an object that carries a discriminator field next to the fields of v. The caller has already
// read the discriminator; here it only has to be accepted as a known field, so it is dropped before the strict decode.
func decodeTagged(raw json.RawMessage, field string, v any) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	delete(obj, field)
	rest, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return decodeStrict(rest, v)
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

// UnmarshalJSON decodes the items by their "kind".
func (t *AudioTrack) UnmarshalJSON(data []byte) error {
	var aux struct {
		ID    string            `json:"id"`
		Items []json.RawMessage `json:"items"`
	}
	if err := decodeStrict(data, &aux); err != nil {
		return err
	}
	t.ID = aux.ID
	t.Items = nil
	if aux.Items != nil {
		t.Items = make([]AudioItem, 0, len(aux.Items))
	}
	for i, raw := range aux.Items {
		it, err := decodeAudioItem(raw)
		if err != nil {
			return fmt.Errorf("items[%d]: %w", i, err)
		}
		t.Items = append(t.Items, it)
	}
	return nil
}

func decodeAudioItem(raw json.RawMessage) (AudioItem, error) {
	var d struct {
		Kind AudioKind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	switch d.Kind {
	case AudioTrendSound:
		var it TrendSound
		if err := decodeTagged(raw, "kind", &it); err != nil {
			return nil, err
		}
		return it, nil
	case AudioLicensedTrack:
		var it LicensedTrack
		if err := decodeTagged(raw, "kind", &it); err != nil {
			return nil, err
		}
		return it, nil
	case AudioCreatorAudio:
		var it CreatorAudio
		if err := decodeTagged(raw, "kind", &it); err != nil {
			return nil, err
		}
		return it, nil
	}
	return nil, fmt.Errorf("unknown audio kind %q", d.Kind)
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
func (a CreatorAudio) MarshalJSON() ([]byte, error) {
	type plain CreatorAudio
	return withTag("kind", string(AudioCreatorAudio), plain(a))
}
