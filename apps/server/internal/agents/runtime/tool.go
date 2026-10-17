package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

// Result is what a tool hands back to the model.
type Result struct {
	// Content is the answer: text blocks and image blocks. An image must name its object in storage (Image.Ref, an R2
	// key); the bytes never go into an event. The llm client resolves the reference when it sends the request.
	Content []llm.Block
	// IsError makes the model see the result as a failure it can act on (a bad argument, a rejected edit).
	IsError bool
	// Final ends the run as succeeded after this result is stored (the finish tool).
	Final bool
	// Ask parks the run in waiting_input instead of answering: Content is the question for the user. The tool call stays
	// unanswered until Service.Reply stores the user's answer as its result (the ask_user tool).
	Ask bool
	// Output is the run's output when Final is set. Empty means {"summary": <the text of Content>}.
	Output json.RawMessage
}

// Text is a result with one text block.
func Text(s string) Result { return Result{Content: []llm.Block{llm.TextBlock(s)}} }

// Errorf is an error result with a message the model can act on.
func Errorf(format string, args ...any) Result {
	return Result{Content: []llm.Block{llm.TextBlock(fmt.Sprintf(format, args...))}, IsError: true}
}

// ToolError is a failure whose message may be shown to the model. A tool returns one (instead of building a Result) when
// the failure is the model's to fix. Any other error from Run is an internal failure: the model sees a generic message,
// the details go to the log, and the event holds no internals.
type ToolError struct{ Msg string }

func (e *ToolError) Error() string { return e.Msg }

// UserErrorf returns a *ToolError.
func UserErrorf(format string, args ...any) error {
	return &ToolError{Msg: fmt.Sprintf(format, args...)}
}

// RunContext is what a tool knows about the run it is called from.
type RunContext struct {
	WorkspaceID ids.WorkspaceID
	RunID       uuid.UUID
	// ProjectID is uuid.Nil for a run without a project.
	ProjectID uuid.UUID
	Agent     string
	// ToolUseID is the model's id of this call.
	ToolUseID string
	// IdempotencyKey is run_id + ":" + tool_use_id. A tool that changes state (Mutates) uses it to make the change once:
	// the same call can reach it twice when a worker dies after the effect and before the result is stored. Store the key
	// with the effect (a unique column) and return the earlier outcome when it is already there.
	IdempotencyKey string
	// Resumed says the runtime already saw this call start: its tool_call event exists but not its result. The effect may
	// or may not have happened; the key is how the tool finds out.
	Resumed bool
	// Input is the run's input (agent_runs.input).
	Input  json.RawMessage
	Logger *slog.Logger

	artifact func(ctx context.Context, payload json.RawMessage) error
}

// WithArtifacts returns a RunContext whose Artifact calls go to sink instead of the run's events. It is for tests of tools,
// which have no run to write to.
func (rc RunContext) WithArtifacts(sink func(ctx context.Context, payload json.RawMessage) error) RunContext {
	rc.artifact = sink
	return rc
}

// Artifact records something the tool made (a timeline version, a render, a file) as an artifact event, for the UI. kind
// names it; data is any JSON-encodable value, small, with images by reference. Calling it twice with the same
// IdempotencyKey is the tool's business: the event is appended each time.
func (rc RunContext) Artifact(ctx context.Context, kind string, data any) error {
	if rc.artifact == nil {
		return errors.New("runtime: this RunContext cannot record artifacts")
	}
	raw, err := json.Marshal(map[string]any{"kind": kind, "tool_use_id": rc.ToolUseID, "data": data})
	if err != nil {
		return fmt.Errorf("runtime: artifact %q: %w", kind, err)
	}
	return rc.artifact(ctx, raw)
}

// Tool is something an agent can call. Implementations are usually built with NewTool.
type Tool interface {
	// Name is what the model calls it: lowercase letters, digits and underscores.
	Name() string
	Description() string
	// Schema is the JSON Schema of the arguments: an object schema that sets additionalProperties to false on every object
	// (the tools are sent to the API with strict: true).
	Schema() json.RawMessage
	// Mutates says the tool changes state outside the run's events. Such a call is made at most once per tool_use id as far
	// as the runtime can ensure, and gets an IdempotencyKey to make its own effect once.
	Mutates() bool
	// Run does the work. input has been checked against Schema. A *ToolError, or a Result with IsError, is shown to the
	// model; any other error is logged and the model gets a generic failure. A context error ends the attempt (the job is
	// retried and resumes).
	Run(ctx context.Context, rc RunContext, input json.RawMessage) (Result, error)
}

// Spec describes a tool built by NewTool.
type Spec struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Mutates     bool
	// NotStrict sends the tool without strict tool use. Strict is the default; a tool opts out only when its schema uses
	// keywords the API does not support under strict (ranges, patterns, oneOf). The arguments are validated against the full
	// schema before the tool runs either way.
	NotStrict bool
}

type typedTool[In any] struct {
	spec Spec
	run  func(ctx context.Context, rc RunContext, in In) (Result, error)
}

// NewTool builds a tool whose handler takes a typed input: the arguments are checked against the schema by the registry,
// then decoded into In (an unknown field is an error).
func NewTool[In any](spec Spec, run func(ctx context.Context, rc RunContext, in In) (Result, error)) Tool {
	return &typedTool[In]{spec: spec, run: run}
}

func (t *typedTool[In]) Name() string            { return t.spec.Name }
func (t *typedTool[In]) Description() string     { return t.spec.Description }
func (t *typedTool[In]) Schema() json.RawMessage { return t.spec.Schema }
func (t *typedTool[In]) Mutates() bool           { return t.spec.Mutates }
func (t *typedTool[In]) Strict() bool            { return !t.spec.NotStrict }

func (t *typedTool[In]) Run(ctx context.Context, rc RunContext, input json.RawMessage) (Result, error) {
	var in In
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Result{}, UserErrorf("the arguments do not fit the tool: %v", err)
	}
	return t.run(ctx, rc, in)
}

var toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Registry holds the tools of an agent: it checks their schemas once, validates every call's arguments against them and
// produces the definitions sent to the API.
type Registry struct {
	tools []Tool
	by    map[string]int
	sch   []*jsonschema.Schema
	defs  []llm.Tool
}

// NewRegistry checks and compiles the tools. A tool name must be unique and plain; a schema must be an object schema, compile, and
// set additionalProperties to false on every object (strict mode).
func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{by: map[string]int{}}
	for i, t := range tools {
		name := t.Name()
		if !toolNameRE.MatchString(name) {
			return nil, fmt.Errorf("runtime: tool name %q must be lowercase letters, digits and underscores", name)
		}
		if _, dup := r.by[name]; dup {
			return nil, fmt.Errorf("runtime: tool %q is registered twice", name)
		}
		if strings.TrimSpace(t.Description()) == "" {
			return nil, fmt.Errorf("runtime: tool %q has no description", name)
		}
		schema := t.Schema()
		strict := true
		if st, ok := t.(interface{ Strict() bool }); ok {
			strict = st.Strict()
		}
		check := checkStrictObject
		if !strict {
			check = checkTopObject // the API does not hold a non-strict schema to the closed-object rule
		}
		if err := check(schema); err != nil {
			return nil, fmt.Errorf("runtime: tool %q: %w", name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
		if err != nil {
			return nil, fmt.Errorf("runtime: tool %q schema: %w", name, err)
		}
		c := jsonschema.NewCompiler()
		url := "tool://" + name + ".json"
		if err := c.AddResource(url, doc); err != nil {
			return nil, fmt.Errorf("runtime: tool %q schema: %w", name, err)
		}
		compiled, err := c.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("runtime: tool %q schema: %w", name, err)
		}
		r.by[name] = i
		r.tools = append(r.tools, t)
		r.sch = append(r.sch, compiled)
		r.defs = append(r.defs, llm.Tool{Name: name, Description: t.Description(), InputSchema: schema, Strict: strict})
	}
	return r, nil
}

// checkTopObject checks that the schema is an object schema that is closed at the top: the arguments of a non-strict tool may
// not carry properties the schema does not name. What is inside it may use any construct the validator understands.
func checkTopObject(raw json.RawMessage) error {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return errors.New("the schema must be a JSON object")
	}
	if typ, _ := root["type"].(string); typ != "object" {
		return errors.New(`the schema must have "type": "object" at the top`)
	}
	if ap, ok := root["additionalProperties"].(bool); !ok || ap {
		return errors.New("the top-level object must set additionalProperties to false")
	}
	return nil
}

// checkStrictObject checks that the schema is an object schema and that every object in it (under properties, items, $defs
// and the combinators) sets additionalProperties to false, which the API requires of a strict tool.
func checkStrictObject(raw json.RawMessage) error {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return errors.New("the schema must be a JSON object")
	}
	if typ, _ := root["type"].(string); typ != "object" {
		return errors.New(`the schema must have "type": "object" at the top`)
	}
	var walk func(path string, n any) error
	walk = func(path string, n any) error {
		m, ok := n.(map[string]any)
		if !ok {
			return nil
		}
		if typ, _ := m["type"].(string); typ == "object" || m["properties"] != nil {
			if ap, ok := m["additionalProperties"].(bool); !ok || ap {
				return fmt.Errorf("strict: %s must set additionalProperties to false", pathOrRoot(path))
			}
		}
		for _, key := range []string{"properties", "$defs", "definitions", "patternProperties"} {
			if sub, ok := m[key].(map[string]any); ok {
				for k, v := range sub {
					if err := walk(path+"/"+key+"/"+k, v); err != nil {
						return err
					}
				}
			}
		}
		for _, key := range []string{"items", "not", "if", "then", "else", "additionalProperties"} {
			if err := walk(path+"/"+key, m[key]); err != nil {
				return err
			}
		}
		for _, key := range []string{"anyOf", "allOf", "oneOf", "prefixItems"} {
			if list, ok := m[key].([]any); ok {
				for i, v := range list {
					if err := walk(fmt.Sprintf("%s/%s/%d", path, key, i), v); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walk("", root)
}

func pathOrRoot(p string) string {
	if p == "" {
		return "the top-level object"
	}
	return "the object at " + p
}

// Definitions returns the tools as the API takes them, in registration order. A tool is strict unless it says otherwise (a
// Strict method that returns false, as Spec.NotStrict does).
func (r *Registry) Definitions() []llm.Tool { return append([]llm.Tool(nil), r.defs...) }

// Get returns the tool with this name.
func (r *Registry) Get(name string) (Tool, bool) {
	i, ok := r.by[name]
	if !ok {
		return nil, false
	}
	return r.tools[i], true
}

// Names lists the tools in registration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.tools))
	for i, t := range r.tools {
		out[i] = t.Name()
	}
	return out
}

// Validate checks a call's arguments against the tool's schema. The error is meant for the model: it names the field and
// what is wrong. An unknown tool is an error too.
func (r *Registry) Validate(name string, input json.RawMessage) error {
	i, ok := r.by[name]
	if !ok {
		return fmt.Errorf("there is no tool named %q; the tools are %s", name, strings.Join(r.Names(), ", "))
	}
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return fmt.Errorf("the arguments are not valid JSON: %w", err)
	}
	err = r.sch[i].Validate(doc)
	if err == nil {
		return nil
	}
	verr, ok := err.(*jsonschema.ValidationError) //nolint:errorlint // Validate returns this type itself, never a wrapper
	if !ok {
		return fmt.Errorf("the arguments could not be checked: %w", err)
	}
	return errors.New("the arguments do not match the tool's schema: " + strings.Join(schemaProblems(verr), "; "))
}

// maxProblems caps how many problems one message lists.
const maxProblems = 8

func schemaProblems(e *jsonschema.ValidationError) []string {
	p := message.NewPrinter(language.English)
	var out []string
	seen := map[string]bool{}
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(out) >= maxProblems {
			return
		}
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		loc := "/" + strings.Join(e.InstanceLocation, "/")
		msg := loc + ": " + e.ErrorKind.LocalizedString(p)
		if !seen[msg] {
			seen[msg] = true
			out = append(out, msg)
		}
	}
	walk(e)
	if len(out) == 0 {
		out = append(out, e.Error())
	}
	return out
}
