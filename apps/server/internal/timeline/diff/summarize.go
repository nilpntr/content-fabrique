package diff

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Summarize describes a patch in words, one line per change, for the run feed and the version list:
//
//	trimmed c3 by 400 ms at the end
//	moved t1 to 2.0 s
//	added clip c7 (posing, asset 899gs, 5.5-8.2 s)
//
// parent is the spec the patch applies to. A JSON Patch carries neither the ids of the items it touches (its paths
// are array indexes) nor the old values, so Summarize replays the patch on a copy of the parent and reads both from it
// as it goes. The at_ms of main-track clips that only moved because of a ripple are one line per shift ("shifted c4,
// c5 by -400 ms"), the markers are counted, and a change the summary has no sentence for is written as the operation
// and its path. An error means the patch does not apply to parent.
func Summarize(parent timeline.Spec, patch Patch) ([]string, error) {
	return SummarizeWith(parent, patch, nil)
}

// Titles names an asset for a summary: it returns the asset's title, or "" when it has none or is not known. A nil
// Titles knows no asset.
type Titles func(assetID string) string

// SummarizeWith is Summarize with a title lookup: a clip is named by its asset's title ("posing") instead of the asset id.
// The end of the asset's id always follows ("posing, asset 899gs"), so two assets with the same title stay apart; an asset
// without a title is named by that alone. The full ids stay in the stored diff and in the spec get_timeline returns.
func SummarizeWith(parent timeline.Spec, patch Patch, titles Titles) ([]string, error) {
	doc, err := json.Marshal(parent)
	if err != nil {
		return nil, fmt.Errorf("diff: encode the spec: %w", err)
	}
	root, err := decode(doc)
	if err != nil {
		return nil, err
	}
	after := clone(root)
	for i, op := range patch {
		if after, err = applyOp(after, op); err != nil {
			return nil, fmt.Errorf("diff: operation %d (%s %q): %w", i, op.Op, op.Path, err)
		}
	}
	s := &summarizer{titles: titles, work: root, after: after, done: map[string]bool{}, moved: map[string]bool{}}
	for i, op := range patch {
		if err := s.describe(op); err != nil {
			// The patch applies (it did above), so this is only a sentence that could not be made.
			s.fallback(op)
		}
		if s.work, err = applyOp(s.work, op); err != nil {
			return nil, fmt.Errorf("diff: operation %d (%s %q): %w", i, op.Op, op.Path, err)
		}
	}
	return s.result(), nil
}

type shift struct {
	id    string
	delta int64
}

type summarizer struct {
	titles Titles
	work   any // the document before the operation being described
	after  any // the document after the whole patch
	lines  []string
	// done holds the (item, field) pairs that are described once from the final state, however many operations touched
	// them.
	done   map[string]bool
	shifts []shift
	// moved holds the clips that were moved to another place: their new at_ms is not a ripple.
	moved map[string]bool
	// Marker changes, counted.
	markersAdded, markersRemoved, markersChanged int
}

func (s *summarizer) say(format string, args ...any) {
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *summarizer) result() []string {
	out := s.lines
	// Ripple: group the clips that moved by the same amount.
	var order []int64
	groups := map[int64][]string{}
	for _, sh := range s.shifts {
		if s.moved[sh.id] {
			continue
		}
		if _, ok := groups[sh.delta]; !ok {
			order = append(order, sh.delta)
		}
		groups[sh.delta] = append(groups[sh.delta], sh.id)
	}
	for _, d := range order {
		out = append(out, fmt.Sprintf("shifted %s by %s", strings.Join(groups[d], ", "), signedMs(d)))
	}
	count := func(n int, what string) {
		if n == 0 {
			return
		}
		if n == 1 {
			out = append(out, what+" 1 marker")
			return
		}
		out = append(out, fmt.Sprintf("%s %d markers", what, n))
	}
	count(s.markersAdded, "added")
	count(s.markersRemoved, "removed")
	count(s.markersChanged, "changed")
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *summarizer) describe(op Operation) error {
	path, err := parsePointer(op.Path)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		s.say("%s the whole timeline", op.Op)
		return nil
	}
	if op.Op == OpMove {
		return s.move(op, path)
	}
	switch path[0] {
	case "tracks":
		return s.tracks(op, path)
	case "markers":
		s.markers(op, path)
		return nil
	case "output", "meta":
		s.say("changed %s%s", strings.Join(path, "."), valueSuffix(op))
		return nil
	}
	s.fallback(op)
	return nil
}

func (s *summarizer) fallback(op Operation) {
	if op.Op == OpMove || op.Op == OpCopy {
		s.say("%s %s to %s", op.Op, op.From, op.Path)
		return
	}
	s.say("%s %s", op.Op, op.Path)
}

// valueSuffix is " to <value>" for an operation that sets a short value.
func valueSuffix(op Operation) string {
	if op.Value == nil {
		return ""
	}
	v := string(op.Value)
	if len(v) > 40 {
		v = v[:37] + "..."
	}
	return " to " + v
}

// ---- tracks and items -------------------------------------------------------------------------------------------

func (s *summarizer) trackAt(tok string) (map[string]any, bool) {
	tracks, ok := get(s.work, "tracks").([]any)
	if !ok {
		return nil, false
	}
	i, err := arrayIndex(tok, len(tracks), false)
	if err != nil {
		return nil, false
	}
	t, ok := tracks[i].(map[string]any)
	return t, ok
}

func (s *summarizer) tracks(op Operation, path []string) error {
	if len(path) == 1 {
		s.fallback(op)
		return nil
	}
	if len(path) == 2 {
		return s.trackLevel(op, path)
	}
	track, ok := s.trackAt(path[1])
	if !ok {
		return fmt.Errorf("no track at %q", path[1])
	}
	typ, _ := track["type"].(string)
	if path[2] != "items" {
		s.trackField(typ, path[2], op)
		return nil
	}
	return s.itemLevel(op, path, track, typ)
}

func (s *summarizer) trackLevel(op Operation, path []string) error {
	switch op.Op {
	case OpAdd, OpReplace:
		v, err := decode(op.Value)
		if err != nil {
			return err
		}
		m, _ := v.(map[string]any)
		typ, _ := m["type"].(string)
		id, _ := m["id"].(string)
		verb := "added"
		if op.Op == OpReplace {
			verb = "replaced"
		}
		if typ == "captions" {
			s.say("%s captions (%s, %s)", verb, str(m["style"]), onOff(m["enabled"]))
			return nil
		}
		s.say("%s %s track %s", verb, typ, id)
		if items, ok := m["items"].([]any); ok {
			for _, it := range items {
				s.addedItem(typ, it)
			}
		}
	case OpRemove:
		track, ok := s.trackAt(path[1])
		if !ok {
			return fmt.Errorf("no track at %q", path[1])
		}
		if str(track["type"]) == "captions" {
			s.say("removed captions")
			return nil
		}
		s.say("removed %s track %s", str(track["type"]), str(track["id"]))
	default:
		s.say("reordered tracks")
	}
	return nil
}

func (s *summarizer) trackField(typ, field string, op Operation) {
	if typ != "captions" {
		s.say("changed %s track %s%s", typ, field, valueSuffix(op))
		return
	}
	val := ""
	if op.Value != nil {
		if v, err := decode(op.Value); err == nil {
			val = scalar(v)
		}
	}
	switch field {
	case "enabled":
		s.say("turned captions %s", map[bool]string{true: "on", false: "off"}[val == "true"])
	case "style":
		s.say("set captions style to %s", val)
	case "uppercase":
		if op.Op == OpRemove {
			s.say("reset captions uppercase to the default")
			return
		}
		s.say("set captions uppercase to %s", val)
	case "max_words":
		if op.Op == OpRemove {
			s.say("reset captions max words to the default")
			return
		}
		s.say("set captions max words to %s", val)
	default:
		s.say("changed captions %s%s", field, valueSuffix(op))
	}
}

func (s *summarizer) itemLevel(op Operation, path []string, track map[string]any, typ string) error {
	items, _ := track["items"].([]any)
	if len(path) == 3 { // the whole items array
		s.fallback(op)
		return nil
	}
	if len(path) == 4 {
		return s.itemWhole(op, path, items, typ)
	}
	i, err := arrayIndex(path[3], len(items), false)
	if err != nil {
		return err
	}
	item, _ := items[i].(map[string]any)
	id := str(item["id"])
	s.itemField(typ, id, path[4:], op, item)
	return nil
}

func (s *summarizer) itemWhole(op Operation, path []string, items []any, typ string) error {
	switch op.Op {
	case OpAdd:
		v, err := decode(op.Value)
		if err != nil {
			return err
		}
		s.addedItem(typ, v)
	case OpRemove:
		i, err := arrayIndex(path[3], len(items), false)
		if err != nil {
			return err
		}
		m, _ := items[i].(map[string]any)
		s.say("removed %s %s", itemNoun(typ), str(m["id"]))
	default:
		s.fallback(op)
	}
	return nil
}

// move describes a move operation. The value that moves is read from the document before the operation, at from, which
// can be anywhere: another place in the same items array (a reorder), another track, or not an item at all.
func (s *summarizer) move(op Operation, path []string) error {
	from, err := parsePointer(op.From)
	if err != nil {
		return err
	}
	val := get(s.work, from...)
	if len(from) == 2 && from[0] == "markers" && path[0] == "markers" {
		return nil // markers are in time order; moving one in the array changes nothing worth a line
	}
	if len(from) == 2 && from[0] == "tracks" && path[0] == "tracks" && len(path) == 2 {
		s.say("reordered tracks")
		return nil
	}
	if len(from) == 4 && from[0] == "tracks" && from[2] == "items" {
		if m, ok := val.(map[string]any); ok {
			track, _ := s.trackAt(from[1])
			id, noun := str(m["id"]), itemNoun(str(track["type"]))
			destTrack := ""
			if len(path) == 4 && path[0] == "tracks" && path[2] == "items" {
				destTrack = path[1]
				if path[1] == from[1] {
					s.moved[id] = true
					if path[3] == "-" {
						s.say("moved %s to the end", id)
						return nil
					}
					if dest, err := strconv.Atoi(path[3]); err == nil {
						s.say("moved %s to position %d", id, dest+1)
						return nil
					}
				}
			}
			if t, ok := s.trackAt(destTrack); ok && destTrack != "" {
				s.moved[id] = true
				s.say("moved %s %s to the %s track %s", noun, id, str(t["type"]), str(t["id"]))
				return nil
			}
			s.say("moved %s %s to %s", noun, id, op.Path)
			return nil
		}
	}
	s.say("moved %s to %s", op.From, op.Path)
	return nil
}

// assetShort is the end of an asset id, "asset 899gs": enough to tell two assets of the same title apart.
func assetShort(id string) string {
	const keep = 6
	if r := []rune(id); len(r) > keep {
		return "asset " + string(r[len(r)-keep:])
	}
	return id
}

// assetTitle is the asset's title when the lookup knows one: one line, at most 40 characters.
func (s *summarizer) assetTitle(id string) string {
	if s.titles == nil {
		return ""
	}
	t := strings.Join(strings.Fields(s.titles(id)), " ")
	if r := []rune(t); len(r) > 40 {
		t = string(r[:37]) + "..."
	}
	return t
}

// assetList names an asset inside a list: "posing, asset 899gs", or "asset 899gs" when it has no title. The id's end is always
// there, so two assets with the same title (or a renamed one) stay apart.
func (s *summarizer) assetList(id string) string {
	if t := s.assetTitle(id); t != "" {
		return t + ", " + assetShort(id)
	}
	return assetShort(id)
}

// assetName names an asset in a sentence: "posing (asset 899gs)", or "asset 899gs".
func (s *summarizer) assetName(id string) string {
	if t := s.assetTitle(id); t != "" {
		return t + " (" + assetShort(id) + ")"
	}
	return assetShort(id)
}

// sourceSuffix is ", 5.5-8.2 s" for the part of the asset a clip plays, or "" when the clip has no source range.
func sourceSuffix(v any) string {
	m, ok := v.(map[string]any)
	if !ok || !isNumber(m["in_ms"]) || !isNumber(m["out_ms"]) {
		return ""
	}
	return fmt.Sprintf(", %s-%s", secsBare(intOf(m["in_ms"])), secs(intOf(m["out_ms"])))
}

func itemNoun(typ string) string {
	switch typ {
	case "video":
		return "clip"
	case "audio":
		return "sound"
	}
	return typ
}

func (s *summarizer) addedItem(typ string, v any) {
	m, _ := v.(map[string]any)
	id := str(m["id"])
	switch typ {
	case "video":
		s.say("added clip %s (%s%s)", id, s.assetList(str(m["asset_id"])), sourceSuffix(m["source"]))
	case "text":
		s.say("added text %s %s at %s", id, quoted(str(m["text"])), secs(intOf(m["at_ms"])))
	case "audio":
		ref := str(m["trend_id"])
		if sfx := str(m["sfx"]); sfx != "" {
			ref = sfx + " at " + secs(intOf(m["at_ms"]))
		} else if ref == "" {
			ref = s.assetName(str(m["asset_id"]))
		}
		s.say("added sound %s (%s %s)", id, str(m["kind"]), ref)
	default:
		s.say("added %s %s", typ, id)
	}
}

// itemField describes a change inside an item. rest is the path below the item ("source", "out_ms").
func (s *summarizer) itemField(typ, id string, rest []string, op Operation, item map[string]any) {
	field := rest[0]
	sub := ""
	if len(rest) > 1 {
		sub = rest[1]
	}
	oldV := get(item, rest...)
	var newV any
	if op.Value != nil {
		newV, _ = decode(op.Value)
	}
	oldN, newN := intOf(oldV), intOf(newV)
	hasNums := isNumber(oldV) && isNumber(newV)

	// A field that is described from the final state, once.
	once := func(group string, f func()) {
		key := id + "/" + group
		if s.done[key] {
			return
		}
		s.done[key] = true
		f()
	}

	switch {
	case field == "at_ms" && typ == "video":
		if hasNums && newN != oldN {
			s.shifts = append(s.shifts, shift{id: id, delta: newN - oldN})
		}
	case field == "at_ms":
		if op.Op == OpReplace || op.Op == OpAdd {
			s.say("moved %s to %s", id, secs(newN))
			return
		}
		s.say("removed the start of %s", id)
	case field == "source" && sub == "in_ms" && hasNums:
		s.edge(id, newN-oldN, true)
	case field == "source" && sub == "out_ms" && hasNums:
		s.edge(id, newN-oldN, false)
	case field == "source":
		once("source", func() {
			if it := findItem(s.after, id); it != nil {
				if m, ok := it["source"].(map[string]any); ok {
					s.say("set the source range of %s to %d to %d ms of its asset", id, intOf(m["in_ms"]), intOf(m["out_ms"]))
					return
				}
			}
			s.say("changed the source range of %s", id)
		})
	case field == "duration_ms" && hasNums:
		s.edge(id, newN-oldN, false)
	case field == "asset_id" && typ == "video":
		s.say("replaced the asset of %s with %s", id, s.assetName(str(newV)))
	case field == "speed":
		if op.Op == OpRemove {
			// A speed that went because a ramp replaced it is not a reset: the ramp's own line says what happened.
			if it := findItem(s.after, id); it != nil {
				if _, ramped := it["speed_ramp"]; ramped {
					return
				}
			}
			s.say("reset the speed of %s to 1x", id)
			return
		}
		s.say("set the speed of %s to %sx", id, scalar(newV))
	case field == "speed_ramp":
		once("speed_ramp", func() { s.finalSpeedRamp(id) })
	case field == "motion":
		once("motion", func() { s.finalMotion(id) })
	case field == "reframe":
		once("reframe", func() { s.finalReframe(id) })
	case field == "exposure_ev":
		if op.Op == OpRemove {
			s.say("removed the exposure hint of %s", id)
			return
		}
		s.say("set the exposure hint of %s to %s EV", id, scalar(newV))
	case field == "look":
		once("look", func() { s.finalLook(id) })
	case field == "transition_in":
		once("transition_in", func() { s.finalTransition(id) })
	case field == "audio" && typ == "video":
		once("audio", func() { s.finalClipAudio(id) })
	case field == "text" && typ == "text":
		s.say("changed the text of %s to %s", id, quoted(str(newV)))
	case field == "style":
		s.say("set the style of %s to %s", id, str(newV))
	case field == "anchor":
		if op.Op != OpRemove {
			s.say("moved %s to the %s", id, strings.ReplaceAll(str(newV), "_", " "))
		}
	case field == "position":
		if op.Op != OpRemove {
			once("position", func() { s.finalPosition(id) })
		}
	case field == "animation":
		if op.Op == OpRemove {
			s.say("removed the animation of %s", id)
			return
		}
		s.say("set the animation of %s to %s", id, str(newV))
	case field == "gain_db":
		if op.Op == OpRemove {
			s.say("reset the gain of %s to 0 dB", id)
			return
		}
		s.say("set the gain of %s to %s dB", id, scalar(newV))
	case field == "fade_in_ms" || field == "fade_out_ms":
		name := "fade in"
		if field == "fade_out_ms" {
			name = "fade out"
		}
		if op.Op == OpRemove {
			s.say("removed the %s of %s", name, id)
			return
		}
		s.say("set the %s of %s to %s ms", name, id, scalar(newV))
	case field == "source_offset_ms":
		s.say("set the source offset of %s to %s", id, secs(newN))
	case field == "why" || field == "moment_id":
		s.say("changed the %s of %s", strings.ReplaceAll(field, "_", " "), id)
	default:
		s.say("changed %s of %s", strings.Join(rest, "."), id)
	}
}

// edge says that an edge of an item moved: the start (in point) or the end (out point, or duration).
func (s *summarizer) edge(id string, delta int64, start bool) {
	if delta == 0 {
		return
	}
	shrinks, where := delta < 0, "end"
	if start {
		shrinks, where = delta > 0, "start"
	}
	verb := "extended"
	if shrinks {
		verb = "trimmed"
	}
	s.say("%s %s by %d ms at the %s", verb, id, abs(delta), where)
}

func (s *summarizer) finalReframe(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	m, ok := it["reframe"].(map[string]any)
	if !ok {
		s.say("removed the reframe of %s", id)
		return
	}
	parts := []string{str(m["mode"])}
	if f, ok := m["focus"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("focus %s/%s", scalar(f["x"]), scalar(f["y"])))
	}
	if z, ok := m["zoom"]; ok {
		parts = append(parts, "zoom "+scalar(z))
	}
	s.say("set the reframe of %s to %s", id, strings.Join(parts, ", "))
}

func (s *summarizer) finalSpeedRamp(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	pts, ok := it["speed_ramp"].([]any)
	if !ok {
		s.say("removed the speed ramp of %s", id)
		return
	}
	parts := make([]string, 0, len(pts))
	for _, p := range pts {
		if m, ok := p.(map[string]any); ok {
			parts = append(parts, fmt.Sprintf("%sx at %s ms", scalar(m["speed"]), scalar(m["at_ms"])))
		}
	}
	s.say("set a speed ramp on %s: %s", id, strings.Join(parts, ", "))
}

func (s *summarizer) finalMotion(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	m, ok := it["motion"].(map[string]any)
	if !ok {
		s.say("removed the motion of %s", id)
		return
	}
	parts := []string{str(m["preset"])}
	if v, ok := m["at_ms"]; ok {
		parts = append(parts, "at "+scalar(v)+" ms")
	}
	if v, ok := m["strength"]; ok {
		parts = append(parts, "strength "+scalar(v))
	}
	s.say("set the motion of %s to %s", id, strings.Join(parts, ", "))
}

func (s *summarizer) finalLook(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	if m, ok := it["look"].(map[string]any); ok {
		if match, _ := m["match"].(bool); match || str(m["preset"]) == timeline.LookAuto {
			s.say("set the look of %s to %s, matching exposure", id, str(m["preset"]))
			return
		}
		s.say("set the look of %s to %s", id, str(m["preset"]))
		return
	}
	s.say("removed the look of %s", id)
}

func (s *summarizer) finalTransition(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	m, ok := it["transition_in"].(map[string]any)
	switch {
	case !ok:
		s.say("removed the transition into %s", id)
	case str(m["type"]) == timeline.TransitionCut:
		s.say("set the transition into %s to a cut", id)
	default:
		s.say("set the transition into %s to %s (%s ms)", id, str(m["type"]), scalar(m["duration_ms"]))
	}
}

func (s *summarizer) finalClipAudio(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	m, ok := it["audio"].(map[string]any)
	if !ok {
		s.say("removed the audio settings of %s", id)
		return
	}
	on := "off"
	if b, _ := m["use_source"].(bool); b {
		on = "on"
	}
	line := fmt.Sprintf("set the source audio of %s %s", id, on)
	if g, ok := m["gain_db"]; ok {
		line += fmt.Sprintf(" at %s dB", scalar(g))
	}
	s.say("%s", line)
}

func (s *summarizer) finalPosition(id string) {
	it := findItem(s.after, id)
	if it == nil {
		return
	}
	if p, ok := it["position"].(map[string]any); ok {
		s.say("moved %s to position %s/%s", id, scalar(p["x"]), scalar(p["y"]))
	}
}

func (s *summarizer) markers(op Operation, path []string) {
	switch {
	case len(path) == 1:
		s.fallback(op)
	case len(path) == 2 && op.Op == OpAdd:
		s.markersAdded++
	case len(path) == 2 && op.Op == OpRemove:
		s.markersRemoved++
	case len(path) == 2 && op.Op == OpMove:
	default:
		s.markersChanged++
	}
}

// ---- helpers ----------------------------------------------------------------------------------------------------

// get follows keys through maps (and numeric tokens through arrays); nil if anything is missing.
func get(node any, path ...string) any {
	for _, tok := range path {
		switch n := node.(type) {
		case map[string]any:
			node = n[tok]
		case []any:
			i, err := arrayIndex(tok, len(n), false)
			if err != nil {
				return nil
			}
			node = n[i]
		default:
			return nil
		}
	}
	return node
}

// findItem returns the item with the given id from any track.
func findItem(doc any, id string) map[string]any {
	tracks, _ := get(doc, "tracks").([]any)
	for _, t := range tracks {
		items, _ := get(t, "items").([]any)
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && str(m["id"]) == id {
				return m
			}
		}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func isNumber(v any) bool {
	_, ok := v.(json.Number)
	return ok
}

func intOf(v any) int64 {
	n, ok := v.(json.Number)
	if !ok {
		return 0
	}
	i, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil {
			return 0
		}
		return int64(f)
	}
	return i
}

// scalar prints a JSON scalar the way a person reads it.
func scalar(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func onOff(v any) string {
	if b, _ := v.(bool); b {
		return "on"
	}
	return "off"
}

func quoted(s string) string {
	s = strings.ReplaceAll(s, "\n", " / ")
	r := []rune(s)
	if len(r) > 40 {
		s = string(r[:37]) + "..."
	}
	return strconv.Quote(s)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func signedMs(d int64) string {
	if d > 0 {
		return fmt.Sprintf("+%d ms", d)
	}
	return fmt.Sprintf("%d ms", d)
}

// secs prints a time in ms as seconds: 2000 as "2.0 s", 2150 as "2.15 s", 2005 as "2.005 s".
func secs(ms int64) string {
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	whole, frac := ms/1000, ms%1000
	switch {
	case frac%100 == 0:
		return fmt.Sprintf("%s%d.%d s", sign, whole, frac/100)
	case frac%10 == 0:
		return fmt.Sprintf("%s%d.%02d s", sign, whole, frac/10)
	}
	return fmt.Sprintf("%s%d.%03d s", sign, whole, frac)
}

// secsBare is secs without the unit, for the start of a range.
func secsBare(ms int64) string { return strings.TrimSuffix(secs(ms), " s") }
