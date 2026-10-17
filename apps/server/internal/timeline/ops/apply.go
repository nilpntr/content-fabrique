package ops

import (
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Apply applies a batch of ops to spec and returns the new spec. It is pure and atomic.
//
// The batch runs, in order, on a copy of spec. When every op could be applied and timeline.Validate finds no error in
// the result, the new spec is returned together with the Result (which may hold warnings, and lists the items the
// batch created). Otherwise Apply returns spec itself, unchanged, and a Result whose issues say what is wrong; each
// issue carries the index of the op that caused it when that is known. The first op that cannot be applied stops the
// batch (the ops after it are not looked at, because they would likely fail for the same reason); problems in the
// resulting spec are all reported at once.
//
// After the ops, the main track is contiguous again: ops that change the length or the order of clips recompute the
// at_ms of the clips after them (the ripple), and a transition shortens the way in. Text and audio items keep their
// absolute times, so one that is left past the end of the timeline is an outside_timeline error. A clip that becomes
// the first one on the main track loses a transition_in. New items get ids from the next free number: c<n> for clips,
// t<n> for text, s<n> for sounds.
//
// The input spec is assumed to be valid. catalog is where asset durations, kinds and rights come from; nil means an
// empty catalog.
func Apply(spec timeline.Spec, batch []Op, catalog timeline.AssetCatalog) (timeline.Spec, Result) {
	fail := func(issues ...Issue) (timeline.Spec, Result) { return spec, Result{Issues: issues} }
	switch {
	case len(batch) == 0:
		return spec, Result{}
	case len(batch) > MaxBatchOps:
		return fail(opIssue(NoOp, "", CodeBatchSize, "", fmt.Sprintf("a batch holds 1 to %d ops (got %d)", MaxBatchOps, len(batch))))
	}
	work, err := cloneSpec(spec)
	if err != nil {
		return fail(opIssue(NoOp, "", CodeInvalidOp, "", err.Error()))
	}
	a := &applier{spec: work, catalog: catalog, touched: map[string]int{}, lastDur: NoOp}
	if a.catalog == nil {
		a.catalog = timeline.MemoryCatalog(nil)
	}
	main, _, ok := timeline.MainTrack(work)
	if !ok {
		return fail(opIssue(NoOp, "", CodeInvalidOp, "", "the timeline has no main track"))
	}
	a.main = main
	a.scanIDs()
	for i, op := range batch {
		a.idx = i
		if !a.dispatch(op) {
			return fail(a.issues...)
		}
	}

	data, err := json.Marshal(work)
	if err != nil {
		return fail(append(a.issues, opIssue(NoOp, "", CodeInvalidOp, "", "the result cannot be encoded as JSON: "+err.Error()))...)
	}
	res := timeline.Validate(data, a.catalog)
	for _, is := range res.Issues {
		idx := a.attribute(work, is.Path)
		op := ""
		if idx >= 0 {
			op = batch[idx].OpName()
		}
		a.issues = append(a.issues, Issue{OpIndex: idx, Op: op, Code: is.Code, Path: is.Path, Message: is.Message, Severity: is.Severity})
	}
	// Created lists what is in the result: an item the batch added and then removed again is not there.
	out := Result{Issues: a.issues, Created: a.finalCreated()}
	if !out.OK() {
		return spec, Result{Issues: a.issues}
	}
	return *work, out
}

// finalCreated is what Apply reports as created: the ids that exist in the result, each once, with the last op that
// created it (an id can be created, removed and created again by a later op).
func (a *applier) finalCreated() []Created {
	last := map[string]int{}
	for i, c := range a.created {
		last[c.ID] = i
	}
	var out []Created
	for i, c := range a.created {
		if last[c.ID] == i && a.idInUse(c.ID) {
			out = append(out, c)
		}
	}
	return out
}

// applier is the state of one Apply.
type applier struct {
	spec      *timeline.Spec
	main      *timeline.VideoTrack
	catalog   timeline.AssetCatalog
	idx       int // the op being applied
	currentOp string
	// kinds maps every id in use (items and tracks) to what it names: "clip", "text", "sound" or "track". It is built once
	// and kept up to date as items are added and removed, so looking an id up never walks the spec.
	kinds map[string]string
	// sorted says that the main track has been put in timeline order (see ensureSorted).
	sorted  bool
	issues  []Issue
	created []Created
	// hw holds, per id prefix ("c", "t", "s"), the highest number seen so far, so an id is never handed out twice in a
	// batch, even after its item was removed.
	hw map[string]int
	// touched maps an item or track id to the last op that changed it; lastDur is the last op that changed the length
	// of the main track. Both only serve to say which op an issue in the result belongs to.
	touched map[string]int
	lastDur int
}

var idNumber = regexp.MustCompile(`^([cts])([0-9]{1,9})$`)

// scanIDs indexes the ids in use and finds the highest number per prefix.
func (a *applier) scanIDs() {
	a.hw = map[string]int{}
	a.kinds = map[string]string{}
	for _, t := range a.spec.Tracks {
		a.register(t.TrackID(), kindTrack)
		switch t := t.(type) {
		case *timeline.VideoTrack:
			for i := range t.Items {
				a.register(t.Items[i].ID, kindClip)
			}
		case *timeline.TextTrack:
			for i := range t.Items {
				a.register(t.Items[i].ID, kindText)
			}
		case *timeline.AudioTrack:
			for _, it := range t.Items {
				a.register(it.Common().ID, kindSound)
			}
		}
	}
}

// What an id can name.
const (
	kindClip  = "clip"
	kindText  = "text"
	kindSound = "sound"
	kindTrack = "track"
)

// register records that id is in use for something of the kind.
func (a *applier) register(id, kind string) {
	a.kinds[id] = kind
	if m := idNumber.FindStringSubmatch(id); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			a.hw[m[1]] = max(a.hw[m[1]], n)
		}
	}
}

// forget records that an item is gone. The high-water mark stays, so its number is not handed out again in this batch.
func (a *applier) forget(id string) { delete(a.kinds, id) }

func (a *applier) idInUse(id string) bool {
	_, ok := a.kinds[id]
	return ok
}

// newID hands out the next free id with the prefix: one more than the highest number seen, skipping any id that is
// somehow taken.
func (a *applier) newID(prefix string) string {
	for {
		a.hw[prefix]++
		id := prefix + strconv.Itoa(a.hw[prefix])
		if !a.idInUse(id) {
			return id
		}
	}
}

// claimID checks an id the op gave for a new item (or hands out a fresh one when it gave none) and registers it. The
// prefix says the kind: "c" for a clip, "t" for text, "s" for a sound.
func (a *applier) claimID(given, prefix string) (string, bool) {
	kind := map[string]string{"c": kindClip, "t": kindText, "s": kindSound}[prefix]
	id := given
	if id == "" {
		id = a.newID(prefix)
	} else if a.idInUse(id) {
		a.errf(CodeIDTaken, "the id %q is already in use (items and tracks share one set of ids); choose another or leave id out to get the next free one", given)
		return "", false
	}
	a.register(id, kind)
	return id, true
}

func isNil(op Op) bool {
	if op == nil {
		return true
	}
	v := reflect.ValueOf(op)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func (a *applier) errf(code timeline.Code, format string, args ...any) bool {
	a.issues = append(a.issues, Issue{
		OpIndex: a.idx, Op: a.currentOp, Code: code, Message: fmt.Sprintf(format, args...), Severity: timeline.SeverityError,
	})
	return false
}

func (a *applier) warnf(code timeline.Code, format string, args ...any) {
	a.issues = append(a.issues, Issue{
		OpIndex: a.idx, Op: a.currentOp, Code: code, Message: fmt.Sprintf(format, args...), Severity: timeline.SeverityWarning,
	})
}

func (a *applier) touch(ids ...string) {
	for _, id := range ids {
		a.touched[id] = a.idx
	}
}

// durationChanged records that the current op may have changed the length of the main track.
func (a *applier) durationChanged() { a.lastDur = a.idx }

func (a *applier) dispatch(op Op) bool {
	if isNil(op) {
		a.currentOp = ""
		return a.errf(CodeInvalidOp, "the op is empty")
	}
	a.currentOp = op.OpName()
	switch o := op.(type) {
	case *AddClip:
		return a.addClip(o)
	case *RemoveItem:
		return a.removeItem(o)
	case *MoveItem:
		return a.moveItem(o)
	case *TrimItem:
		return a.trimItem(o)
	case *ReplaceSource:
		return a.replaceSource(o)
	case *SetSpeed:
		return a.setSpeed(o)
	case *SetReframe:
		return a.setReframe(o)
	case *AddText:
		return a.addText(o)
	case *UpdateText:
		return a.updateText(o)
	case *RemoveText:
		return a.removeText(o)
	case *SetTransition:
		return a.setTransition(o)
	case *SetSound:
		return a.setSound(o)
	case *RemoveSound:
		return a.removeSound(o)
	case *SnapCutsToBeats:
		return a.snapCuts(o)
	case *SetLook:
		return a.setLook(o)
	case *SetCaptions:
		return a.setCaptions(o)
	case *SetClipAudio:
		return a.setClipAudio(o)
	case *SetMotion:
		return a.setMotion(o)
	case *SetSpeedRamp:
		return a.setSpeedRamp(o)
	}
	return a.errf(CodeUnknownOp, "unsupported op type %T", op)
}

// ---- the main track ---------------------------------------------------------------------------------------------

// ensureSorted puts the main track in timeline order, once, before the first op that works on the positions of clips
// or on their timing. A valid spec may hold its clips out of order as long as they are contiguous by time; an op that
// does not look at the order of clips leaves the array as it is, so it does not produce moves in the diff.
func (a *applier) ensureSorted() {
	if !a.sorted {
		a.sorted = true
		sortMainItems(a.main)
	}
}

// sortMainItems puts a main track in timeline order (by at_ms; equal times keep their order).
func sortMainItems(main *timeline.VideoTrack) {
	slices.SortStableFunc(main.Items, func(x, y timeline.VideoItem) int { return cmp.Compare(x.AtMs, y.AtMs) })
}

// ripple recomputes the at_ms of the main track from the durations: the first clip is at 0 and each next one starts
// where the previous one ends, minus its own transition. A transition longer than the clip before it would start the
// clip before 0; at_ms stops at 0 (the validator reports the transition). A clip that plays nothing (in_ms not below
// out_ms) is left out of the contiguity check by the validator, which reports it as source_out_of_range; it sits where
// the next clip starts and takes no time.
func (a *applier) ripple() {
	var end int64
	for i := range a.main.Items {
		it := &a.main.Items[i]
		if !timeline.Playable(*it) {
			it.AtMs = end
			continue
		}
		it.AtMs = max(0, end-it.TransitionDurationMs())
		end = it.AtMs + timeline.OutputDuration(*it)
	}
	a.durationChanged()
}

// firstClipLosesTransition clears the transition_in of the first clip when it is not the clip that was first before
// the op, because nothing comes before the first clip to transition from.
func (a *applier) firstClipLosesTransition(prevFirstID string) {
	if len(a.main.Items) == 0 || a.main.Items[0].ID == prevFirstID {
		return
	}
	if t := a.main.Items[0].TransitionIn; t != nil && t.Type != timeline.TransitionCut {
		a.main.Items[0].TransitionIn = nil
		a.touch(a.main.Items[0].ID)
	}
}

func (a *applier) firstClipID() string {
	if len(a.main.Items) == 0 {
		return ""
	}
	return a.main.Items[0].ID
}

// ---- finding things ---------------------------------------------------------------------------------------------

// kindOf says what an id names: "clip", "text", "sound", "track", or "" if nothing.
func (a *applier) kindOf(id string) string { return a.kinds[id] }

func (a *applier) textTrack() *timeline.TextTrack {
	for _, t := range a.spec.Tracks {
		if v, ok := t.(*timeline.TextTrack); ok {
			return v
		}
	}
	return nil
}

func (a *applier) audioTrack() *timeline.AudioTrack {
	for _, t := range a.spec.Tracks {
		if v, ok := t.(*timeline.AudioTrack); ok {
			return v
		}
	}
	return nil
}

func (a *applier) captionsTrack() *timeline.CaptionsTrack {
	for _, t := range a.spec.Tracks {
		if v, ok := t.(*timeline.CaptionsTrack); ok {
			return v
		}
	}
	return nil
}

func (a *applier) ids(kind string) string {
	var ids []string
	switch kind {
	case "clip":
		for _, it := range a.main.Items {
			ids = append(ids, it.ID)
		}
	case "text":
		if t := a.textTrack(); t != nil {
			for _, it := range t.Items {
				ids = append(ids, it.ID)
			}
		}
	case "sound":
		if t := a.audioTrack(); t != nil {
			for _, it := range t.Items {
				ids = append(ids, it.Common().ID)
			}
		}
	}
	if len(ids) == 0 {
		return "there are none"
	}
	if len(ids) > 20 {
		ids = append(ids[:20], "...")
	}
	return "there are " + strings.Join(ids, ", ")
}

// wrongKind reports an id that names nothing, or something of another kind than want (kindClip, kindText or
// kindSound).
func (a *applier) wrongKind(id, want string) bool {
	noun := want
	if want == kindText {
		noun = "text item"
	}
	switch got := a.kindOf(id); got {
	case "":
		return a.errf(CodeUnknownItem, "there is no %s with id %q; %s", noun, id, a.ids(want))
	default:
		return a.errf(CodeWrongItemKind, "%q is a %s, not a %s; %s", id, got, noun, a.hintFor(got, noun))
	}
}

func (a *applier) hintFor(got, want string) string {
	switch got {
	case "text":
		return "use update_text, remove_text, move_item (at_ms) or trim_item (duration_ms) for text"
	case "sound":
		return "use set_sound, remove_sound, move_item (at_ms) or trim_item (duration_ms) for sounds"
	case "clip":
		return "clips are on the main track and have ops of their own (trim_item, set_speed, ...)"
	}
	return fmt.Sprintf("an op for a %s needs the id of a %s", want, want)
}

// clip finds a clip by id and returns its index on the main track.
func (a *applier) clip(id string) (int, bool) {
	if id != "" {
		for i := range a.main.Items {
			if a.main.Items[i].ID == id {
				return i, true
			}
		}
	}
	return 0, a.wrongKind(id, kindClip)
}

func (a *applier) textItem(id string) (*timeline.TextItem, bool) {
	if t := a.textTrack(); t != nil && id != "" {
		for i := range t.Items {
			if t.Items[i].ID == id {
				return &t.Items[i], true
			}
		}
	}
	return nil, a.wrongKind(id, kindText)
}

// soundIndex finds a sound by id and returns its index on the audio track.
func (a *applier) soundIndex(id string) (int, bool) {
	if t := a.audioTrack(); t != nil && id != "" {
		for i, it := range t.Items {
			if it.Common().ID == id {
				return i, true
			}
		}
	}
	return 0, a.wrongKind(id, kindSound)
}

// commonOf gives write access to the shared fields of an audio item.
func commonOf(item timeline.AudioItem) *timeline.AudioCommon {
	switch v := item.(type) {
	case *timeline.TrendSound:
		return &v.AudioCommon
	case *timeline.LicensedTrack:
		return &v.AudioCommon
	case *timeline.CreatorAudio:
		return &v.AudioCommon
	}
	return nil
}

// freeTrackID returns base, or base_2, base_3, ... whichever is not in use and is not avoid (the id of an item that is
// about to be added).
func (a *applier) freeTrackID(base, avoid string) string {
	id := base
	for n := 2; id == avoid || a.idInUse(id); n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	return id
}

// ---- blame ------------------------------------------------------------------------------------------------------

var itemPath = regexp.MustCompile(`^/tracks/([0-9]+)(?:/items/([0-9]+))?`)

// attribute says which op an issue of the validator belongs to: the last op that touched the item or track the path
// points into. A problem with something no op touched (a text item that now runs past the end, the total duration) is
// blamed on the last op that changed the length of the main track.
func (a *applier) attribute(spec *timeline.Spec, path string) int {
	m := itemPath.FindStringSubmatch(path)
	if m == nil {
		if strings.HasPrefix(path, "/markers") {
			if i, ok := a.touched["markers"]; ok {
				return i
			}
		}
		return a.lastDur
	}
	ti, _ := strconv.Atoi(m[1])
	if ti >= len(spec.Tracks) {
		return a.lastDur
	}
	t := spec.Tracks[ti]
	id := t.TrackID()
	if m[2] != "" {
		ii, _ := strconv.Atoi(m[2])
		switch t := t.(type) {
		case *timeline.VideoTrack:
			if ii < len(t.Items) {
				id = t.Items[ii].ID
			}
		case *timeline.TextTrack:
			if ii < len(t.Items) {
				id = t.Items[ii].ID
			}
		case *timeline.AudioTrack:
			if ii < len(t.Items) {
				id = t.Items[ii].Common().ID
			}
		}
	}
	if i, ok := a.touched[id]; ok {
		return i
	}
	return a.lastDur
}
