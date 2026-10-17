package ops

import (
	"bytes"
	_ "embed" // the schema is embedded
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The embedded schema is a copy of packages/schemas/timeline/ops.v1.schema.json, the source of truth. `task gen` (or
// `go generate ./internal/timeline/ops`) refreshes the copy and CI fails if it differs.
//
//go:generate cp ../../../../../packages/schemas/timeline/ops.v1.schema.json schema/ops.v1.schema.json
//go:embed schema/ops.v1.schema.json
var schemaJSON []byte

// SchemaJSON returns the embedded JSON Schema of the edit ops. The editor agent's edit_timeline tool contract is built
// from it.
func SchemaJSON() []byte { return bytes.Clone(schemaJSON) }

const schemaURL = "https://contentfabrique.app/schemas/timeline/ops.v1.json"

// soundKinds are the variants of set_sound, in the order of the schema's oneOf.
var soundKinds = []string{string(timeline.AudioTrendSound), string(timeline.AudioLicensedTrack), string(timeline.AudioCreatorAudio)}

// compiledOps holds the compiled schema of each op, and of each variant of set_sound.
var compiledOps = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("ops schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("ops schema: %w", err)
	}
	out := make(map[string]*jsonschema.Schema, len(Names)+len(soundKinds))
	for _, name := range Names {
		if name == NameSetSound {
			for i, k := range soundKinds {
				s, err := c.Compile(fmt.Sprintf("%s#/$defs/set_sound/oneOf/%d", schemaURL, i))
				if err != nil {
					return nil, fmt.Errorf("ops schema: set_sound %s: %w", k, err)
				}
				out[NameSetSound+"/"+k] = s
			}
			continue
		}
		s, err := c.Compile(schemaURL + "#/$defs/" + name)
		if err != nil {
			return nil, fmt.Errorf("ops schema: %s: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
})

// hints replace the library's wording for the keywords that say "one of these" (oneOf, anyOf, not), which do not tell
// a model what to change. The key is the op name and the pointer into the op.
var hints = map[string]string{
	NameAddClip + "|":       "give at most one of index and after_id",
	NameMoveItem + "|":      "give exactly one of at_ms (for a text item or sound), index and after_id (for a clip)",
	NameTrimItem + "|":      "give in_ms and/or out_ms (for a clip) or duration_ms (for a text item or sound)",
	NameAddText + "|":       "give exactly one of anchor and position",
	NameUpdateText + "|":    "give at least one field to change, and not both anchor and position",
	NameSetTransition + "|": "cut takes no duration_ms; every other type (crossfade, dip_to_black, whip, whip_left, whip_right, zoom_through, flash) needs a duration_ms from 100 to 1000",
	NameSetCaptions + "|":   "give at least one of enabled, style, uppercase and max_words",
	NameSetClipAudio + "|":  "give at least one of use_source and gain_db",
}

// checkOpDoc validates one decoded op (numbers as json.Number) against its schema and returns the problems as issues
// for the op at index idx. It returns nil when the op is structurally valid.
func checkOpDoc(doc any, idx int) []Issue {
	obj, ok := doc.(map[string]any)
	if !ok {
		return []Issue{opIssue(idx, "", CodeInvalidOp, "", "an op must be a JSON object with an \"op\" field")}
	}
	name, ok := obj["op"].(string)
	if !ok {
		return []Issue{opIssue(idx, "", CodeInvalidOp, "/op", "the op has no \"op\" field naming it; it must be one of "+strings.Join(Names, ", "))}
	}
	if !slices.Contains(Names, name) {
		return []Issue{opIssue(idx, name, CodeUnknownOp, "/op", fmt.Sprintf("unknown op %q; the ops are %s", name, strings.Join(Names, ", ")))}
	}
	schemas, err := compiledOps()
	if err != nil {
		return []Issue{opIssue(idx, name, CodeInvalidOp, "", err.Error())}
	}
	key := name
	if name == NameSetSound {
		k, _ := obj["kind"].(string)
		if !slices.Contains(soundKinds, k) {
			return []Issue{opIssue(idx, name, CodeInvalidOp, "/kind", fmt.Sprintf("kind must be one of %s", strings.Join(soundKinds, ", ")))}
		}
		key = name + "/" + k
	}
	err = schemas[key].Validate(doc)
	if err == nil {
		return nil
	}
	verr, ok := err.(*jsonschema.ValidationError) //nolint:errorlint // Validate returns this type itself, never a wrapper
	if !ok {
		return []Issue{opIssue(idx, name, CodeInvalidOp, "", err.Error())}
	}
	w := &walker{idx: idx, op: name, printer: message.NewPrinter(language.English), seen: map[string]bool{}}
	w.walk(verr)
	return w.issues
}

func opIssue(idx int, op string, code timeline.Code, field, msg string) Issue {
	return Issue{OpIndex: idx, Op: op, Code: code, Field: field, Message: msg, Severity: timeline.SeverityError}
}

// maxFieldIssues caps the structural issues of one op.
const maxFieldIssues = 20

type walker struct {
	idx     int
	op      string
	printer *message.Printer
	issues  []Issue
	seen    map[string]bool
}

func (w *walker) add(loc []string, msg string) {
	field := pointer(loc)
	key := field + "\x00" + msg
	if w.seen[key] || len(w.issues) >= maxFieldIssues {
		return
	}
	w.seen[key] = true
	w.issues = append(w.issues, opIssue(w.idx, w.op, CodeInvalidOp, field, msg))
}

func (w *walker) walk(e *jsonschema.ValidationError) {
	switch e.ErrorKind.(type) {
	case *kind.OneOf, *kind.AnyOf, *kind.Not:
		w.alternatives(e)
		return
	}
	if len(e.Causes) > 0 {
		for _, c := range e.Causes {
			w.walk(c)
		}
		return
	}
	w.add(e.InstanceLocation, w.text(e))
}

// alternatives handles a oneOf, anyOf or not that failed. A hint written for the place wins; otherwise the branch
// with the fewest problems is reported, which is the one the author most likely meant.
func (w *walker) alternatives(e *jsonschema.ValidationError) {
	if h, ok := hints[w.op+"|"+pointer(e.InstanceLocation)]; ok {
		w.add(e.InstanceLocation, h)
		return
	}
	if len(e.Causes) == 0 {
		w.add(e.InstanceLocation, e.ErrorKind.LocalizedString(w.printer))
		return
	}
	best, bestN := e.Causes[0], leafCount(e.Causes[0])
	for _, c := range e.Causes[1:] {
		if n := leafCount(c); n < bestN {
			best, bestN = c, n
		}
	}
	w.walk(best)
}

func leafCount(e *jsonschema.ValidationError) int {
	if len(e.Causes) == 0 {
		return 1
	}
	n := 0
	for _, c := range e.Causes {
		n += leafCount(c)
	}
	return n
}

// text is the message of a leaf error: the library's wording, with the property the error is about in front, and
// numbers written as plain decimals.
func (w *walker) text(e *jsonschema.ValidationError) string {
	name := ""
	if n := len(e.InstanceLocation); n > 0 {
		name = e.InstanceLocation[n-1]
	}
	var msg string
	switch k := e.ErrorKind.(type) {
	case *kind.Minimum:
		msg = fmt.Sprintf("must be at least %s (got %s)", ratString(k.Want), ratString(k.Got))
	case *kind.Maximum:
		msg = fmt.Sprintf("must be at most %s (got %s)", ratString(k.Want), ratString(k.Got))
	case *kind.MinLength:
		msg = fmt.Sprintf("must be at least %d characters (got %d)", k.Want, k.Got)
	case *kind.MaxLength:
		msg = fmt.Sprintf("must be at most %d characters (got %d)", k.Want, k.Got)
	case *kind.Type:
		msg = fmt.Sprintf("must be of type %s (got %s)", strings.Join(k.Want, " or "), k.Got)
		if k.Got == "number" && slices.Contains(k.Want, "integer") {
			msg += "; write whole numbers without a fraction"
		}
	case *kind.Pattern:
		switch name {
		case "text":
			msg = "must be 1 to 120 characters on at most 3 lines (separated by a newline), with no other control characters and no U+2028 or U+2029"
		default:
			msg = fmt.Sprintf("%q does not match the required format %s", k.Got, k.Want)
		}
	default:
		// "missing property", "additional properties" and the like already name their subject.
		return e.ErrorKind.LocalizedString(w.printer)
	}
	if name == "" {
		return msg
	}
	return name + " " + msg
}

func ratString(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	return strings.TrimRight(strings.TrimRight(r.FloatString(6), "0"), ".")
}

// pointer formats an instance location as a JSON Pointer; the root is the empty string.
func pointer(loc []string) string {
	var b strings.Builder
	for _, part := range loc {
		b.WriteByte('/')
		part = strings.ReplaceAll(part, "~", "~0")
		part = strings.ReplaceAll(part, "/", "~1")
		b.WriteString(part)
	}
	return b.String()
}
