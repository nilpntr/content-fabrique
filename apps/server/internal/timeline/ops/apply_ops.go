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

func cloneVideoAudio(v *timeline.VideoAudio) *timeline.VideoAudio {
	if v == nil {
		return nil
	}
	return &timeline.VideoAudio{UseSource: copyPtr(v.UseSource), GainDB: copyPtr(v.GainDB)}
}

// ---- add_clip, remove_item, move_item ---------------------------------------------------------------------------

func (a *applier) addClip(o *AddClip) bool {
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
		Speed: copyPtr(o.Speed), Reframe: cloneReframe(o.Reframe), Look: copyPtr(o.Look),
		TransitionIn: copyPtr(o.TransitionIn), Audio: cloneVideoAudio(o.Audio), Why: copyPtr(o.Why),
	}
	a.main.Items = slices.Insert(a.main.Items, pos, it)
	a.ripple()
	a.touch(id)
	a.created = append(a.created, Created{OpIndex: a.idx, Kind: "clip", ID: id})
	return true
}

func (a *applier) removeItem(o *RemoveItem) bool {
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	prevFirst := a.firstClipID()
	a.main.Items = slices.Delete(a.main.Items, i, i+1)
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
		commonOf(a.audioTrack().Items[i]).AtMs = *o.AtMs
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
			return a.wrongKind(o.AfterID, "clip")
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
		i, _ := a.clip(o.ID)
		it := &a.main.Items[i]
		if o.InMs != nil {
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
		commonOf(a.audioTrack().Items[i]).DurationMs = copyPtr(o.DurationMs)
	case "track":
		return a.errf(CodeWrongItemKind, "%q is a track; tracks cannot be trimmed", o.ID)
	default:
		return a.errf(CodeUnknownItem, "there is no clip, text item or sound with id %q", o.ID)
	}
	a.touch(o.ID)
	return true
}

func (a *applier) replaceSource(o *ReplaceSource) bool {
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	it := &a.main.Items[i]
	it.AssetID, it.Source, it.MomentID = o.AssetID, o.Source, o.MomentID
	a.ripple()
	a.touch(o.ID)
	return true
}

func (a *applier) setSpeed(o *SetSpeed) bool {
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	v := o.Speed
	a.main.Items[i].Speed = &v
	a.ripple()
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
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	t := &timeline.Transition{Type: o.Type, DurationMs: o.DurationMs}
	if o.Type == timeline.TransitionCut {
		if o.DurationMs != 0 {
			return a.errf(CodeBadArgument, "a cut has no duration; leave duration_ms out, or choose crossfade, dip_to_black or whip")
		}
		t.DurationMs = 0
	}
	a.main.Items[i].TransitionIn = t
	a.ripple()
	a.touch(o.ID)
	return true
}

func (a *applier) setLook(o *SetLook) bool {
	if o.ID == "" {
		for i := range a.main.Items {
			a.main.Items[i].Look = &timeline.Look{Preset: o.Preset}
			a.touch(a.main.Items[i].ID)
		}
		return true
	}
	i, ok := a.clip(o.ID)
	if !ok {
		return false
	}
	a.main.Items[i].Look = &timeline.Look{Preset: o.Preset}
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
		a.spec.Tracks = append(a.spec.Tracks, at)
		a.touch(at.ID)
	}
	at.Items = append(at.Items, build(id))
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
