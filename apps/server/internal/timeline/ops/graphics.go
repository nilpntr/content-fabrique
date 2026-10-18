package ops

import (
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The graphics ops (D-35, docs/design/motion-graphics-v1.md): motion-graphics overlays on the graphics track. An item is a template of
// the library with its params; it keeps its absolute time when clips move, like text.

// Op names of the graphics ops.
const (
	NameAddGraphic    = "add_graphic"
	NameUpdateGraphic = "update_graphic"
	NameRemoveGraphic = "remove_graphic"
)

// kindGraphic is what the id of a graphics item names.
const kindGraphic = "graphic"

// AddGraphic adds a graphics item. Exactly one of Anchor and Position is set.
type AddGraphic struct {
	ID           string                  `json:"id,omitempty"`
	Template     string                  `json:"template"`
	AtMs         int64                   `json:"at_ms"`
	DurationMs   int64                   `json:"duration_ms"`
	Anchor       string                  `json:"anchor,omitempty"`
	Position     *timeline.Point         `json:"position,omitempty"`
	AvoidSubject *bool                   `json:"avoid_subject,omitempty"`
	Params       timeline.GraphicsParams `json:"params"`
}

// UpdateGraphic changes the fields of a graphics item that are set. Params (with Template) replaces all of the item's params; a new
// AtMs without Params moves the item's accents with it.
type UpdateGraphic struct {
	ID           string                   `json:"id"`
	Template     string                   `json:"template,omitempty"`
	AtMs         *int64                   `json:"at_ms,omitempty"`
	DurationMs   *int64                   `json:"duration_ms,omitempty"`
	Anchor       string                   `json:"anchor,omitempty"`
	Position     *timeline.Point          `json:"position,omitempty"`
	AvoidSubject *bool                    `json:"avoid_subject,omitempty"`
	Params       *timeline.GraphicsParams `json:"params,omitempty"`
}

// RemoveGraphic removes a graphics item.
type RemoveGraphic struct {
	ID string `json:"id"`
}

// OpName implements Op.
func (*AddGraphic) OpName() string { return NameAddGraphic }

// OpName implements Op.
func (*UpdateGraphic) OpName() string { return NameUpdateGraphic }

// OpName implements Op.
func (*RemoveGraphic) OpName() string { return NameRemoveGraphic }

// MarshalJSON writes the op with its "op" field.
func (o AddGraphic) MarshalJSON() ([]byte, error) {
	type p AddGraphic
	return tagged(NameAddGraphic, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o UpdateGraphic) MarshalJSON() ([]byte, error) {
	type p UpdateGraphic
	return tagged(NameUpdateGraphic, p(o))
}

// MarshalJSON writes the op with its "op" field.
func (o RemoveGraphic) MarshalJSON() ([]byte, error) {
	type p RemoveGraphic
	return tagged(NameRemoveGraphic, p(o))
}

func (a *applier) graphicsTrack() *timeline.GraphicsTrack {
	for _, t := range a.spec.Tracks {
		if v, ok := t.(*timeline.GraphicsTrack); ok {
			return v
		}
	}
	return nil
}

func (a *applier) graphicItem(id string) (*timeline.GraphicsItem, bool) {
	if t := a.graphicsTrack(); t != nil && id != "" {
		for i := range t.Items {
			if t.Items[i].ID == id {
				return &t.Items[i], true
			}
		}
	}
	return nil, a.wrongKind(id, kindGraphic)
}

func (a *applier) addGraphic(o *AddGraphic) bool {
	id, ok := a.claimID(o.ID, "g")
	if !ok {
		return false
	}
	gt := a.graphicsTrack()
	if gt == nil {
		gt = &timeline.GraphicsTrack{ID: a.freeTrackID("graphics", id)}
		a.register(gt.ID, kindTrack)
		a.spec.Tracks = append(a.spec.Tracks, gt)
		a.touch(gt.ID)
	}
	gt.Items = append(gt.Items, timeline.GraphicsItem{
		ID: id, Template: o.Template, AtMs: o.AtMs, DurationMs: o.DurationMs, Anchor: o.Anchor, Position: copyPtr(o.Position),
		AvoidSubject: copyPtr(o.AvoidSubject), Params: cloneParams(o.Params),
	})
	a.touch(id)
	a.created = append(a.created, Created{OpIndex: a.idx, Kind: kindGraphic, ID: id})
	return true
}

func (a *applier) updateGraphic(o *UpdateGraphic) bool {
	if o.Template == "" && o.AtMs == nil && o.DurationMs == nil && o.Anchor == "" && o.Position == nil && o.AvoidSubject == nil && o.Params == nil {
		return a.errf(CodeBadArgument, "give at least one field to change on the graphics item %q", o.ID)
	}
	if o.Anchor != "" && o.Position != nil {
		return a.errf(CodeBadArgument, "give anchor or position, not both")
	}
	if (o.Params == nil) != (o.Template == "") {
		return a.errf(CodeBadArgument, "params replaces all of the item's params, so give template and params together (the same template to keep it)")
	}
	g, ok := a.graphicItem(o.ID)
	if !ok {
		return false
	}
	if o.AtMs != nil {
		if o.Params == nil {
			// The accents are timeline times on the item: they move with it.
			for i := range g.Params.AccentsMs {
				g.Params.AccentsMs[i] += *o.AtMs - g.AtMs
			}
		}
		g.AtMs = *o.AtMs
	}
	if o.DurationMs != nil {
		g.DurationMs = *o.DurationMs
	}
	if o.Anchor != "" {
		g.Anchor, g.Position = o.Anchor, nil
	}
	if o.Position != nil {
		g.Anchor, g.Position = "", copyPtr(o.Position)
	}
	if o.AvoidSubject != nil {
		g.AvoidSubject = copyPtr(o.AvoidSubject)
	}
	if o.Params != nil {
		g.Template, g.Params = o.Template, cloneParams(*o.Params)
	}
	a.touch(o.ID)
	return true
}

func (a *applier) removeGraphic(o *RemoveGraphic) bool {
	if _, ok := a.graphicItem(o.ID); !ok {
		return false
	}
	gt := a.graphicsTrack()
	gt.Items = slices.DeleteFunc(gt.Items, func(g timeline.GraphicsItem) bool { return g.ID == o.ID })
	a.forget(o.ID)
	a.touch(o.ID)
	return true
}

// cloneParams copies the params so the op and the spec share nothing.
func cloneParams(p timeline.GraphicsParams) timeline.GraphicsParams {
	p.Scale, p.Number, p.Word, p.From, p.To, p.Decimals = copyPtr(p.Scale), copyPtr(p.Number), copyPtr(p.Word), copyPtr(p.From), copyPtr(p.To), copyPtr(p.Decimals)
	p.AccentsMs = slices.Clone(p.AccentsMs)
	return p
}
