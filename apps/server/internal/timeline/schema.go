package timeline

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
)

// The embedded schema is a copy of packages/schemas/timeline/v1.schema.json, the source of truth. `task gen` (or
// `go generate ./internal/timeline`) refreshes the copy and CI fails if it differs.
//
//go:generate cp ../../../../packages/schemas/timeline/v1.schema.json schema/v1.schema.json
//go:embed schema/v1.schema.json
var schemaJSON []byte

// SchemaJSON returns the embedded JSON Schema of the timeline spec. The editor agent's tool contract is built from it.
func SchemaJSON() []byte { return bytes.Clone(schemaJSON) }

const schemaURL = "https://contentfabrique.app/schemas/timeline/v1.json"

// maxSchemaIssues caps the structural issues of one spec, so a pathological document cannot produce a huge answer.
const maxSchemaIssues = 50

// compiled is the schema, compiled once.
var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("timeline schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("timeline schema: %w", err)
	}
	s, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("timeline schema: %w", err)
	}
	return s, nil
})

// validateStructure runs layer 1 on a decoded document (numbers as json.Number) and maps the schema's errors to
// issues.
func validateStructure(doc any) []Issue {
	sch, err := compiled()
	if err != nil {
		return []Issue{{Code: CodeSchema, Severity: SeverityError, Message: err.Error()}}
	}
	err = sch.Validate(doc)
	if err == nil {
		return nil
	}
	verr, ok := err.(*jsonschema.ValidationError) //nolint:errorlint // Validate returns this type itself, never a wrapper
	if !ok {
		return []Issue{{Code: CodeSchema, Severity: SeverityError, Message: err.Error()}}
	}
	c := &schemaIssues{doc: doc, printer: message.NewPrinter(language.English), seen: map[string]bool{}}
	c.walk(verr)
	return c.issues
}

// schemaIssues turns the error tree of the jsonschema library into a flat, deduplicated list of issues. The tree is
// deep: every $ref adds a level, and a oneOf has one subtree per branch, so one mistake in a track or an audio item
// shows up once per branch. The rule that keeps the output readable (design note section 3): when the variants are
// told apart by a discriminator ("type" or "kind") and the instance names one of them, only that variant's errors
// are reported.
type schemaIssues struct {
	doc     any
	printer *message.Printer
	issues  []Issue
	seen    map[string]bool
}

func (c *schemaIssues) add(loc []string, msg string) {
	path := pointer(loc)
	key := path + "\x00" + msg
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	if len(c.issues) == maxSchemaIssues {
		c.issues = append(c.issues, Issue{Code: CodeSchema, Severity: SeverityError,
			Message: fmt.Sprintf("more than %d structural problems; fix these first", maxSchemaIssues)})
	}
	if len(c.issues) > maxSchemaIssues {
		return
	}
	c.issues = append(c.issues, Issue{Code: CodeSchema, Path: path, Message: msg, Severity: SeverityError})
}

func (c *schemaIssues) walk(e *jsonschema.ValidationError) {
	if _, isOneOf := e.ErrorKind.(*kind.OneOf); isOneOf {
		c.oneOf(e)
		return
	}
	if len(e.Causes) > 0 {
		for _, cause := range e.Causes {
			c.walk(cause)
		}
		return
	}
	c.leaf(e)
}

// text is the message of a leaf error. The library's own wording is kept where it is clear; it is replaced where it
// prints numbers in scientific notation or with thousands separators, or where a regular expression would reach the
// model. The property the error is about is put in front, because the path alone is easy to misread.
func (c *schemaIssues) text(e *jsonschema.ValidationError) string {
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
		if name == "text" {
			msg = "must be 1 to 120 characters on at most 3 lines (separated by a newline), with no other control characters and no U+2028 or U+2029"
		} else {
			msg = fmt.Sprintf("%q does not match the required format %s", k.Got, k.Want)
		}
	case *kind.Const, *kind.Enum:
		msg = e.ErrorKind.LocalizedString(c.printer)
		if loc := e.InstanceLocation; len(loc) >= 3 && loc[len(loc)-1] == "type" && loc[len(loc)-2] == "transition_in" && loc[len(loc)-3] == "0" {
			msg = "of the first clip must be \"cut\": nothing comes before it to transition from. Remove transition_in or make it {\"type\": \"cut\"}"
		}
	default:
		// "missing property", "additional properties" and the like already name their subject.
		return e.ErrorKind.LocalizedString(c.printer)
	}
	if name == "" {
		return msg
	}
	return name + " " + msg
}

// ratString prints a number from the schema (a limit or the offending value) as a plain decimal.
func ratString(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	return strings.TrimRight(strings.TrimRight(r.FloatString(6), "0"), ".")
}

func (c *schemaIssues) leaf(e *jsonschema.ValidationError) {
	switch e.ErrorKind.(type) {
	case *kind.Contains, *kind.MinContains, *kind.MaxContains:
		if len(e.InstanceLocation) == 1 && e.InstanceLocation[0] == "tracks" {
			c.add(e.InstanceLocation, "tracks must hold exactly one video track (id \"main\") and at most one text, one captions and one audio track")
			return
		}
	}
	c.add(e.InstanceLocation, c.text(e))
}

// discriminators are the property names that tell the variants of a oneOf apart, in the order they are tried.
var discriminators = []string{"type", "kind"}

func (c *schemaIssues) oneOf(e *jsonschema.ValidationError) {
	loc := e.InstanceLocation
	if len(e.Causes) == 0 {
		// More than one variant matched.
		c.add(loc, c.ambiguous(loc))
		return
	}
	for _, key := range discriminators {
		var selected []*jsonschema.ValidationError
		var allowed []string
		mismatches := 0
		for _, branch := range e.Causes {
			bad, want := discriminatorMismatch(branch, loc, key)
			if bad {
				mismatches++
				allowed = append(allowed, want...)
				continue
			}
			selected = append(selected, branch)
		}
		if mismatches == 0 {
			continue // this key does not tell the variants apart
		}
		if len(selected) == 0 {
			c.add(loc, c.unknownVariant(loc, key, allowed))
			return
		}
		for _, branch := range selected {
			c.walk(branch)
		}
		return
	}
	c.add(loc, c.noVariant(loc, e.Causes))
}

// discriminatorMismatch reports whether a oneOf branch failed because the instance's discriminator is not the one
// the branch requires, and which values the branch accepts.
func discriminatorMismatch(branch *jsonschema.ValidationError, loc []string, key string) (bool, []string) {
	var want []string
	found := false
	var visit func(e *jsonschema.ValidationError)
	visit = func(e *jsonschema.ValidationError) {
		for _, cause := range e.Causes {
			visit(cause)
		}
		if len(e.Causes) > 0 || !slices.Equal(e.InstanceLocation, append(slices.Clone(loc), key)) {
			return
		}
		switch k := e.ErrorKind.(type) {
		case *kind.Const:
			found = true
			want = append(want, fmt.Sprint(k.Want))
		case *kind.Enum:
			found = true
			for _, w := range k.Want {
				want = append(want, fmt.Sprint(w))
			}
		}
	}
	visit(branch)
	return found, want
}

// unknownVariant is the message when the instance has a discriminator that no variant accepts, or none.
func (c *schemaIssues) unknownVariant(loc []string, key string, allowed []string) string {
	obj, _ := valueAt(c.doc, loc).(map[string]any)
	if obj == nil {
		return fmt.Sprintf("must be an object with a %q of %s", key, quoteList(allowed))
	}
	if _, has := obj[key]; !has {
		return fmt.Sprintf("missing property %q; it must be one of %s", key, quoteList(allowed))
	}
	return fmt.Sprintf("%q must be one of %s", key, quoteList(allowed))
}

// ambiguous is the message when a oneOf matched more than one variant. Only the text item has such a oneOf (anchor
// and position), and the output (never, the format/size pairs are exclusive).
func (c *schemaIssues) ambiguous(loc []string) string {
	if obj, ok := valueAt(c.doc, loc).(map[string]any); ok {
		if _, a := obj["anchor"]; a {
			if _, p := obj["position"]; p {
				return "has both anchor and position; set exactly one of them"
			}
		}
	}
	return "matches more than one of the allowed shapes; it must match exactly one"
}

// noVariant is the message when none of the variants matched and no discriminator can say which one was meant.
func (c *schemaIssues) noVariant(loc []string, branches []*jsonschema.ValidationError) string {
	if _, isObject := valueAt(c.doc, loc).(map[string]any); !isObject {
		return "must be an object"
	}
	if len(loc) == 1 && loc[0] == "output" {
		return "width and height must be the size of the format: 9:16 is 1080x1920, 1:1 is 1080x1080, 4:5 is 1080x1350, 16:9 is 1920x1080"
	}
	if obj, ok := valueAt(c.doc, loc).(map[string]any); ok {
		_, a := obj["anchor"]
		_, p := obj["position"]
		if !a && !p {
			if _, hasText := obj["text"]; hasText {
				return "has neither anchor nor position; set exactly one of them"
			}
		}
	}
	var alts []string
	for _, b := range branches {
		var parts []string
		collectLeafTexts(b, c.printer, &parts)
		if s := strings.Join(parts, " and "); s != "" && !slices.Contains(alts, s) {
			alts = append(alts, s)
		}
	}
	return "does not match any of the allowed shapes: " + strings.Join(alts, " | ")
}

func collectLeafTexts(e *jsonschema.ValidationError, p *message.Printer, out *[]string) {
	if len(e.Causes) == 0 {
		*out = append(*out, fmt.Sprintf("%s: %s", pointer(e.InstanceLocation), e.ErrorKind.LocalizedString(p)))
		return
	}
	for _, c := range e.Causes {
		collectLeafTexts(c, p, out)
	}
}

func quoteList(values []string) string {
	seen := map[string]bool{}
	var q []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			q = append(q, fmt.Sprintf("%q", v))
		}
	}
	return strings.Join(q, ", ")
}

// valueAt follows an instance location through a decoded document.
func valueAt(doc any, loc []string) any {
	cur := doc
	for _, part := range loc {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[part]
		case []any:
			var idx int
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 0 || idx >= len(v) {
				return nil
			}
			cur = v[idx]
		default:
			return nil
		}
	}
	return cur
}

// pointer formats an instance location as a JSON Pointer (RFC 6901). The root is the empty string.
func pointer(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range loc {
		b.WriteByte('/')
		part = strings.ReplaceAll(part, "~", "~0")
		part = strings.ReplaceAll(part, "/", "~1")
		b.WriteString(part)
	}
	return b.String()
}
