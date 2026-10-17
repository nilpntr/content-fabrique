package editor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// The features the editor's prompt may offer. They are computed from the edit-op schema the build embeds (internal/timeline/ops),
// never written by hand, so the prompt can never offer an op, a transition, a motion preset or a look that does not exist: a
// section of the prompt that teaches an effect sits behind a feature, and the feature is there only when the schema has it.
//
// Feature names:
//
//	op:<name>          an edit op, such as op:set_motion or op:add_sfx
//	transition:<type>  a transition type, such as transition:flash
//	motion:<preset>    a motion preset, such as motion:punch_in
//	look:<preset>      a look preset, such as look:gym_moody
//	sfx                generated sound effects: some op adds one (op:add_sfx)
//	looks              the exposure-aware looks: look:auto or look:gym_moody
//
// Looks and sound effects are sections of the prompt and the playbooks behind their features: they were written before part B of P3-10
// (the `auto` and `gym_moody` looks, `add_sfx`) merged and turned on when the schema got them, with no change to the text.
type Features struct{ set map[string]bool }

// Has reports whether the feature exists.
func (f Features) Has(name string) bool { return f.set[name] }

// List returns the features, sorted.
func (f Features) List() []string {
	out := make([]string, 0, len(f.set))
	for k := range f.set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// NewFeatures builds a Features from names (for tests, and for FeaturesFromSchema).
func NewFeatures(names ...string) Features {
	f := Features{set: map[string]bool{}}
	for _, n := range names {
		f.set[n] = true
	}
	return f
}

// AvailableFeatures are the features of the embedded op schema.
var AvailableFeatures = sync.OnceValue(func() Features {
	f, err := FeaturesFromSchema(ops.SchemaJSON())
	if err != nil {
		// The schema is embedded and tested, so this is a build problem; offering nothing is the safe answer.
		panic(fmt.Sprintf("editor: the embedded op schema has no features: %v", err))
	}
	return f
})

// FeaturesFromSchema reads the op schema (the JSON of ops.v1.schema.json) and lists what it offers.
func FeaturesFromSchema(schema []byte) (Features, error) {
	var doc struct {
		OneOf []struct {
			Ref string `json:"$ref"`
		} `json:"oneOf"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return Features{}, err
	}
	f := NewFeatures()
	for _, o := range doc.OneOf {
		if name, ok := strings.CutPrefix(o.Ref, "#/$defs/"); ok {
			f.set["op:"+name] = true
		}
	}
	if len(f.set) == 0 {
		return Features{}, fmt.Errorf("the schema lists no ops")
	}
	for _, v := range enumsAt(doc.Defs, "transition", "oneOf", "*", "properties", "type") {
		f.set["transition:"+v] = true
	}
	for _, v := range enumsAt(doc.Defs, "set_motion", "properties", "preset") {
		f.set["motion:"+v] = true
	}
	looks := enumsAt(doc.Defs, "set_look", "properties", "preset")
	looks = append(looks, enumsAt(doc.Defs, "look", "properties", "preset")...)
	for _, v := range looks {
		f.set["look:"+v] = true
	}
	for k := range f.set {
		if strings.HasPrefix(k, "op:") && strings.Contains(k, "sfx") {
			f.set["sfx"] = true
		}
	}
	if f.set["look:auto"] || f.set["look:gym_moody"] {
		f.set["looks"] = true
	}
	return f, nil
}

// enumsAt follows path from $defs/<def> and returns the strings of the "enum" (or the single "const") it finds there. A "*" step
// goes through every element of an array.
func enumsAt(defs map[string]json.RawMessage, def string, path ...string) []string {
	raw, ok := defs[def]
	if !ok {
		return nil
	}
	var node any
	if json.Unmarshal(raw, &node) != nil {
		return nil
	}
	nodes := []any{node}
	for _, step := range path {
		var next []any
		for _, n := range nodes {
			switch v := n.(type) {
			case map[string]any:
				if c, ok := v[step]; ok {
					next = append(next, c)
				}
			case []any:
				if step == "*" {
					next = append(next, v...)
				}
			}
		}
		nodes = next
	}
	var out []string
	for _, n := range nodes {
		m, ok := n.(map[string]any)
		if !ok {
			continue
		}
		if list, ok := m["enum"].([]any); ok {
			for _, e := range list {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		}
		if s, ok := m["const"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}
