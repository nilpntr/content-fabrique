package ops

import (
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The ops that work on one item or one setting. snap_cuts_to_beats is in snap.go.

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneReframe(r *timeline.Reframe) *timeline.Reframe {
	if r == nil {
		return nil
	}
	return &timeline.Reframe{Mode: r.Mode, Focus: copyPtr(r.Focus), Zoom: copyPtr(r.Zoom)}
}

func cloneLook(l *timeline.Look) *timeline.Look {
	if l == nil {
		return nil
	}
	return &timeline.Look{Preset: l.Preset, Match: copyPtr(l.Match)}
}

func cloneMotion(m *timeline.Motion) *timeline.Motion {
	if m == nil {
		return nil
	}
	return &timeline.Motion{Preset: m.Preset, AtMs: copyPtr(m.AtMs), Strength: copyPtr(m.Strength)}
}

func cloneVideoAudio(v *timeline.VideoAudio) *timeline.VideoAudio {
	if v == nil {
		return nil
	}
	return &timeline.VideoAudio{UseSource: copyPtr(v.UseSource), GainDB: copyPtr(v.GainDB)}
}

// ---- add_clip, remove_item, move_item ---------------------------------------------------------------------------

func (a *applier) addClip(o *AddClip) bool {
	a.ensureSorted()
	items := a.main.Items
	if o.Index != nil && o.AfterID != "" {
		return a.errf(CodeBadArgument, "give index or after_id, not both")
	}
	pos := len(items)
	switch {
	case o.Index != nil:
		pos = *o.Index
		if pos < 0 || pos > len(items) {
			return a.errf(CodeBadPosition, "index %d is out of range: the main track has %d clips, so a new clip can go at 0 to %d (%d appends)",
				pos, len(items), len(items), len(items))
		}
	case o.AfterID != "":
		k, ok := a.clip(o.AfterID)
		if !ok {
			return false
		}
		pos = k + 1
	}
	id, ok := a.claimID(o.ID, "c")
	if !ok {
		return false
	}
	it := timeline.VideoItem{
		ID: id, AssetID: o.AssetID, MomentID: o.MomentID, Source: o.Source,
		Speed: copyPtr(o.Speed), SpeedRamp: slices.Clone(o.SpeedRamp), Reframe: cloneReframe(o.Reframe), Motion: cloneMotion(o.Motion), Look: cloneLook(o.Look), ExposureEv: copyPtr(o.ExposureEv),
		TransitionIn: copyPtr(o.TransitionIn), Audio: cloneVideoAudio(o.Audio), Why: copyPtr(o.Why),
	}
	a.main.Items = slices.Insert(a.main.Items, pos, it)
	a.ripple()
	a.touch(id)
	a.created = append(a.created, Created{OpIndex: a.idx, Kind: "clip", ID: id})
	return true
}

func (a *applier) removeItem(o *RemoveItem) bool {
	a.ensureSorted()
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	prevFirst := a.firstClipID()
	a.main.Items = slices.Delete(a.main.Items, i, i+1)
	a.forget(o.ID)
	a.firstClipLosesTransition(prevFirst)
	a.ripple()
	return true
}

func (a *applier) moveItem(o *MoveItem) bool {
	given := 0
	if o.AtMs != nil {
		given++
	}
	if o.Index != nil {
		given++
	}
	if o.AfterID != "" {
		given++
	}
	if given != 1 {
		return a.errf(CodeBadArgument, "give exactly one of at_ms (for a text item or sound), index and after_id (for a clip); got %d", given)
	}
	switch a.kindOf(o.ID) {
	case "clip":
		return a.moveClip(o)
	case "text":
		if o.AtMs == nil {
			return a.errf(CodeBadArgument, "%q is a text item; move it with at_ms, its new start in ms", o.ID)
		}
		ti, _ := a.textItem(o.ID)
		ti.AtMs = *o.AtMs
		a.touch(o.ID)
		return true
	case "sound":
		if o.AtMs == nil {
			return a.errf(CodeBadArgument, "%q is a sound; move it with at_ms, its new start in ms", o.ID)
		}
		i, _ := a.soundIndex(o.ID)
		setSoundAt(a.audioTrack().Items[i], *o.AtMs)
		a.touch(o.ID)
		return true
	case "track":
		return a.errf(CodeWrongItemKind, "%q is a track; tracks cannot be moved", o.ID)
	}
	return a.errf(CodeUnknownItem, "there is no clip, text item or sound with id %q", o.ID)
}

func (a *applier) moveClip(o *MoveItem) bool {
	if o.AtMs != nil {
		return a.errf(CodeBadArgument, "a clip's start is worked out from its place on the main track; move %q with index or after_id", o.ID)
	}
	a.ensureSorted()
	from, _ := a.clip(o.ID)
	it := a.main.Items[from]
	rest := slices.Delete(slices.Clone(a.main.Items), from, from+1)
	dest := 0
	switch {
	case o.Index != nil:
		dest = *o.Index
		if dest < 0 || dest > len(rest) {
			return a.errf(CodeBadPosition, "index %d is out of range: the main track has %d clips, so a clip can move to 0 to %d",
				dest, len(rest)+1, len(rest))
		}
	default:
		if o.AfterID == o.ID {
			return a.errf(CodeBadPosition, "cannot place %q after itself", o.ID)
		}
		k := slices.IndexFunc(rest, func(v timeline.VideoItem) bool { return v.ID == o.AfterID })
		if k < 0 {
			return a.wrongKind(o.AfterID, kindClip)
		}
		dest = k + 1
	}
	prevFirst := a.firstClipID()
	a.main.Items = slices.Insert(rest, dest, it)
	a.firstClipLosesTransition(prevFirst)
	a.ripple()
	a.touch(o.ID)
	return true
}

// ---- trim_item, replace_source, set_speed, ... ------------------------------------------------------------------

func (a *applier) trimItem(o *TrimItem) bool {
	switch a.kindOf(o.ID) {
	case "clip":
		if o.DurationMs != nil {
			return a.errf(CodeBadArgument, "%q is a clip; trim it with in_ms and/or out_ms (the new source range), not duration_ms", o.ID)
		}
		if o.InMs == nil && o.OutMs == nil {
			return a.errf(CodeBadArgument, "give in_ms and/or out_ms to trim the clip %q", o.ID)
		}
		a.ensureSorted()
		i, _ := a.clip(o.ID)
		it := &a.main.Items[i]
		if o.InMs != nil {
			if it.HasRamp() {
				// The ramp's points count from the in point: move them with it, so the ramp stays on the same footage.
				shiftRamp(it, *o.InMs-it.Source.InMs)
			}
			it.Source.InMs = *o.InMs
		}
		if o.OutMs != nil {
			it.Source.OutMs = *o.OutMs
		}
		a.ripple()
	case "text":
		if o.DurationMs == nil || o.InMs != nil || o.OutMs != nil {
			return a.errf(CodeBadArgument, "%q is a text item; trim it with duration_ms, its new length in ms", o.ID)
		}
		ti, _ := a.textItem(o.ID)
		ti.DurationMs = *o.DurationMs
	case "sound":
		if o.DurationMs == nil || o.InMs != nil || o.OutMs != nil {
			return a.errf(CodeBadArgument, "%q is a sound; trim it with duration_ms, its new length in ms", o.ID)
		}
		i, _ := a.soundIndex(o.ID)
		if _, isSFX := a.audioTrack().Items[i].(*timeline.SFX); isSFX {
			return a.errf(CodeBadArgument, "%q is a sound effect, which plays for its own length (cut at the end of the timeline); it cannot be trimmed. Remove it and add another effect, or move it with at_ms", o.ID)
		}
		commonOf(a.audioTrack().Items[i]).DurationMs = copyPtr(o.DurationMs)
	case "track":
		return a.errf(CodeWrongItemKind, "%q is a track; tracks cannot be trimmed", o.ID)
	default:
		return a.errf(CodeUnknownItem, "there is no clip, text item or sound with id %q", o.ID)
	}
	a.touch(o.ID)
	return true
}

// shiftRamp moves the speed ramp of a clip whose in point moves by delta ms (positive: later), so that every point stays on
// the footage it was set for. The points are offsets from the in point, so each moves by -delta. A point that falls before the
// new in point is dropped and the ramp starts there at the speed it had (linear between the points on either side of it); if
// every point is gone the clip plays at the speed the ramp ended with, as a plain speed. Moving the in point earlier needs
// nothing: the clip plays at the first point's speed before it.
func shiftRamp(it *timeline.VideoItem, delta int64) {
	if delta <= 0 {
		for i := range it.SpeedRamp {
			it.SpeedRamp[i].AtMs -= delta
		}
		return
	}
	pts := it.SpeedRamp
	first := 0 // the index of the first point that stays (at or after the new in point)
	for first < len(pts) && pts[first].AtMs-delta < 0 {
		first++
	}
	if first == 0 {
		for i := range pts {
			pts[i].AtMs -= delta
		}
		return
	}
	if first == len(pts) { // all of the ramp is behind the new in point
		v := pts[len(pts)-1].Speed
		it.SpeedRamp, it.Speed = nil, &v
		return
	}
	prev, next := pts[first-1], pts[first]
	speedAt0 := prev.Speed
	if span := float64(next.AtMs - prev.AtMs); span > 0 {
		speedAt0 = prev.Speed + (next.Speed-prev.Speed)*float64(delta-prev.AtMs)/span
	}
	out := make([]timeline.SpeedPoint, 0, len(pts)-first+1)
	if next.AtMs-delta > 0 {
		out = append(out, timeline.SpeedPoint{AtMs: 0, Speed: speedAt0})
	}
	for _, p := range pts[first:] {
		out = append(out, timeline.SpeedPoint{AtMs: p.AtMs - delta, Speed: p.Speed})
	}
	if len(out) < timeline.MinRampPoints { // the tail of the ramp is one point: a constant speed
		v := out[0].Speed
		it.SpeedRamp, it.Speed = nil, &v
		return
	}
	it.SpeedRamp = out
}

func (a *applier) replaceSource(o *ReplaceSource) bool {
	a.ensureSorted()
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	it := &a.main.Items[i]
	it.AssetID, it.Source, it.MomentID = o.AssetID, o.Source, o.MomentID
	it.ExposureEv = nil // a hint measured on other footage means nothing on this
	a.ripple()
	a.touch(o.ID)
	return true
}

func (a *applier) setSpeed(o *SetSpeed) bool {
	a.ensureSorted()
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	v := o.Speed
	a.main.Items[i].Speed = &v
	a.main.Items[i].SpeedRamp = nil // a ramp replaces the speed, and so does a speed the ramp
	a.ripple()
	a.touch(o.ID)
	return true
}

func (a *applier) setSpeedRamp(o *SetSpeedRamp) bool {
	a.ensureSorted()
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	it := &a.main.Items[i]
	it.SpeedRamp = slices.Clone(o.Points)
	it.Speed = nil // the ramp replaces the speed
	a.ripple()
	a.touch(o.ID)
	return true
}

func (a *applier) setMotion(o *SetMotion) bool {
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	if o.Preset == timeline.MotionNone {
		if o.AtMs != nil || o.Strength != nil {
			return a.errf(CodeBadArgument, "the preset none takes no at_ms or strength; leave them out, or choose punch_in, push_in, pull_back or shake")
		}
		a.main.Items[i].Motion = nil
	} else {
		a.main.Items[i].Motion = &timeline.Motion{Preset: o.Preset, AtMs: copyPtr(o.AtMs), Strength: copyPtr(o.Strength)}
	}
	a.touch(o.ID)
	return true
}

func (a *applier) setReframe(o *SetReframe) bool {
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	a.main.Items[i].Reframe = &timeline.Reframe{Mode: o.Mode, Focus: copyPtr(o.Focus), Zoom: copyPtr(o.Zoom)}
	a.touch(o.ID)
	return true
}

func (a *applier) setTransition(o *SetTransition) bool {
	a.ensureSorted()
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	t := &timeline.Transition{Type: o.Type, DurationMs: o.DurationMs}
	if o.Type == timeline.TransitionCut {
		if o.DurationMs != 0 {
			return a.errf(CodeBadArgument, "a cut has no duration; leave duration_ms out, or choose crossfade, dip_to_black, whip_left, whip_right, zoom_through or flash")
		}
		t.DurationMs = 0
	}
	a.main.Items[i].TransitionIn = t
	a.ripple()
	a.touch(o.ID)
	return true
}

// newLook is the look a set_look gives a clip: the preset, and the clip's match unless the op gives one.
func newLook(old *timeline.Look, o *SetLook) *timeline.Look {
	l := &timeline.Look{Preset: o.Preset, Match: copyPtr(o.Match)}
	if l.Match == nil && old != nil {
		l.Match = copyPtr(old.Match)
	}
	return l
}

func (a *applier) setLook(o *SetLook) bool {
	if o.ID == "" {
		if o.ExposureEv != nil {
			return a.errf(CodeBadArgument, "exposure_ev is the hint of one clip; give id to set it")
		}
		for i := range a.main.Items {
			a.main.Items[i].Look = newLook(a.main.Items[i].Look, o)
			a.touch(a.main.Items[i].ID)
		}
		return true
	}
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	a.main.Items[i].Look = newLook(a.main.Items[i].Look, o)
	if o.ExposureEv != nil {
		a.main.Items[i].ExposureEv = copyPtr(o.ExposureEv)
		if *o.ExposureEv == 0 { // 0 clears the hint
			a.main.Items[i].ExposureEv = nil
		}
	}
	a.touch(o.ID)
	return true
}

func (a *applier) setClipAudio(o *SetClipAudio) bool {
	if o.UseSource == nil && o.GainDB == nil {
		return a.errf(CodeBadArgument, "give at least one of use_source and gain_db")
	}
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	it := &a.main.Items[i]
	audio := cloneVideoAudio(it.Audio)
	if audio == nil {
		audio = &timeline.VideoAudio{}
	}
	if o.UseSource != nil {
		audio.UseSource = copyPtr(o.UseSource)
	}
	if o.GainDB != nil {
		audio.GainDB = copyPtr(o.GainDB)
	}
	it.Audio = audio
	a.touch(o.ID)
	return true
}

// ---- text -------------------------------------------------------------------------------------------------------

func (a *applier) addText(o *AddText) bool {
	id, ok := a.claimID(o.ID, "t")
	if !ok {
		return false
	}
	tt := a.textTrack()
	if tt == nil {
		tt = &timeline.TextTrack{ID: a.freeTrackID("text", id)}
		a.register(tt.ID, kindTrack)
		a.spec.Tracks = append(a.spec.Tracks, tt)
		a.touch(tt.ID)
	}
	tt.Items = append(tt.Items, timeline.TextItem{
		ID: id, AtMs: o.AtMs, DurationMs: o.DurationMs, Text: o.Text, Style: o.Style,
		Anchor: o.Anchor, Position: copyPtr(o.Position), Animation: o.Animation,
	})
	a.touch(id)
	a.created = append(a.created, Created{OpIndex: a.idx, Kind: "text", ID: id})
	return true
}

func (a *applier) updateText(o *UpdateText) bool {
	if o.AtMs == nil && o.DurationMs == nil && o.Text == nil && o.Style == "" && o.Anchor == "" && o.Position == nil && o.Animation == "" {
		return a.errf(CodeBadArgument, "give at least one field to change on the text item %q", o.ID)
	}
	if o.Anchor != "" && o.Position != nil {
		return a.errf(CodeBadArgument, "give anchor or position, not both")
	}
	ti, ok := a.textItem(o.ID)
	if !ok {
		return false
	}
	if o.AtMs != nil {
		ti.AtMs = *o.AtMs
	}
	if o.DurationMs != nil {
		ti.DurationMs = *o.DurationMs
	}
	if o.Text != nil {
		ti.Text = *o.Text
	}
	if o.Style != "" {
		ti.Style = o.Style
	}
	if o.Anchor != "" {
		ti.Anchor, ti.Position = o.Anchor, nil
	}
	if o.Position != nil {
		ti.Anchor, ti.Position = "", copyPtr(o.Position)
	}
	if o.Animation != "" {
		ti.Animation = o.Animation
	}
	a.touch(o.ID)
	return true
}

func (a *applier) removeText(o *RemoveText) bool {
	if _, ok := a.textItem(o.ID); !ok {
		return false
	}
	tt := a.textTrack()
	tt.Items = slices.DeleteFunc(tt.Items, func(t timeline.TextItem) bool { return t.ID == o.ID })
	a.forget(o.ID)
	a.touch(o.ID)
	return true
}

// ---- sounds and captions ----------------------------------------------------------------------------------------

func (a *applier) setSound(o *SetSound) bool {
	var export string
	switch o.Kind {
	case timeline.AudioTrendSound:
		export = timeline.ExportCueOnly
	case timeline.AudioLicensedTrack, timeline.AudioCreatorAudio:
		export = timeline.ExportLicensed
	default:
		return a.errf(CodeBadArgument, "kind %q is not a sound kind; use trend_sound, licensed_track or creator_audio", o.Kind)
	}
	common := timeline.AudioCommon{
		Export:     export,
		DurationMs: copyPtr(o.DurationMs), GainDB: copyPtr(o.GainDB), FadeInMs: copyPtr(o.FadeInMs), FadeOutMs: copyPtr(o.FadeOutMs),
	}
	if o.AtMs != nil {
		common.AtMs = *o.AtMs
	}
	if o.SourceOffsetMs != nil {
		common.SourceOffsetMs = *o.SourceOffsetMs
	}
	build := func(id string) timeline.AudioItem {
		common.ID = id
		switch o.Kind {
		case timeline.AudioTrendSound:
			return &timeline.TrendSound{AudioCommon: common, TrendID: o.TrendID}
		case timeline.AudioLicensedTrack:
			return &timeline.LicensedTrack{AudioCommon: common, AssetID: o.AssetID, LicenseID: o.LicenseID}
		}
		return &timeline.CreatorAudio{AudioCommon: common, AssetID: o.AssetID}
	}

	at := a.audioTrack()
	if o.ID != "" && at != nil {
		for i, it := range at.Items {
			if it.Common().ID == o.ID {
				at.Items[i] = build(o.ID)
				a.touch(o.ID)
				return true
			}
		}
	}
	id, ok := a.claimID(o.ID, "s")
	if !ok {
		return false
	}
	if at == nil {
		at = &timeline.AudioTrack{ID: a.freeTrackID("sound", id)}
		a.register(at.ID, kindTrack)
		a.spec.Tracks = append(a.spec.Tracks, at)
		a.touch(at.ID)
	}
	at.Items = append(at.Items, build(id))
	a.touch(id)
	a.created = append(a.created, Created{OpIndex: a.idx, Kind: "sound", ID: id})
	return true
}

func (a *applier) addSFX(o *AddSFX) bool {
	if _, ok := timeline.SFXLengthMs(o.Sfx); !ok {
		return a.errf(CodeBadArgument, "sfx %q is not a sound effect; use whoosh, impact, riser or swish", o.Sfx)
	}
	id, ok := a.claimID(o.ID, "s")
	if !ok {
		return false
	}
	at := a.audioTrack()
	if at == nil {
		at = &timeline.AudioTrack{ID: a.freeTrackID("sound", id)}
		a.register(at.ID, kindTrack)
		a.spec.Tracks = append(a.spec.Tracks, at)
		a.touch(at.ID)
	}
	at.Items = append(at.Items, &timeline.SFX{ID: id, Sfx: o.Sfx, AtMs: o.AtMs, Export: timeline.ExportLicensed, GainDB: copyPtr(o.GainDB)})
	a.touch(id)
	a.created = append(a.created, Created{OpIndex: a.idx, Kind: "sound", ID: id})
	return true
}

func (a *applier) removeSound(o *RemoveSound) bool {
	i, ok := a.soundIndex(o.ID)
	if !ok {
		return false
	}
	at := a.audioTrack()
	at.Items = slices.Delete(at.Items, i, i+1)
	a.forget(o.ID)
	a.touch(o.ID)
	return true
}

func (a *applier) setCaptions(o *SetCaptions) bool {
	if o.Enabled == nil && o.Style == "" && o.Uppercase == nil && o.MaxWords == nil {
		return a.errf(CodeBadArgument, "give at least one of enabled, style, uppercase and max_words")
	}
	ct := a.captionsTrack()
	if ct == nil {
		ct = &timeline.CaptionsTrack{ID: a.freeTrackID("captions", ""), From: "transcript", Style: "word_highlight", Enabled: true}
		a.register(ct.ID, kindTrack)
		a.spec.Tracks = append(a.spec.Tracks, ct)
	}
	if o.Enabled != nil {
		ct.Enabled = *o.Enabled
	}
	if o.Style != "" {
		ct.Style = o.Style
	}
	if o.Uppercase != nil {
		ct.Uppercase = copyPtr(o.Uppercase)
	}
	if o.MaxWords != nil {
		ct.MaxWords = copyPtr(o.MaxWords)
	}
	a.touch(ct.ID)
	return true
}
