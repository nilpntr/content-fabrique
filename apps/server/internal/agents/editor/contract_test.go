package editor

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// The contract test (P4-02 acceptance: "a JSON-schema contract test"). packages/schemas/agents/editor-tools.v1.json is the source of
// truth for what the model sees; the TypeScript side (editor-tools.v1.test.ts) compiles the same file with Ajv.

const sourceContract = "../../../../../packages/schemas/agents/editor-tools.v1.json"

func TestEmbeddedContractIsTheSourceFile(t *testing.T) {
	src, err := os.ReadFile(sourceContract)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, ContractJSON()) {
		t.Fatal("the embedded copy of editor-tools.v1.json differs from packages/schemas/agents/editor-tools.v1.json: run `task gen` (go generate ./internal/agents/...) and commit the result")
	}
}

// Each tool the agent registers is exactly the tool of the contract: its name, description, strict flag and (bundled) schema, in
// the order of the file; and the contract has no tool the agent lacks.
func TestRegisteredToolsAreTheContract(t *testing.T) {
	e := newEnv(t)
	defs, err := Contract()
	if err != nil {
		t.Fatal(err)
	}
	got := e.reg.Definitions()
	if len(got) != len(defs) {
		t.Fatalf("the agent registers %d tools, the contract lists %d", len(got), len(defs))
	}
	wantNames := []string{"get_brief", "list_footage", "view_clip", "find_moments", "get_music", "plan_edit", "get_timeline", "get_comments", "edit_timeline", "match_exposure", "render_preview", "check_edit", "finish"}
	for i, d := range defs {
		g := got[i]
		if g.Name != d.Name || g.Description != d.Description || g.Strict != d.Strict {
			t.Errorf("tool %d: registered %q (strict %v) differs from the contract %q (strict %v)", i, g.Name, g.Strict, d.Name, d.Strict)
		}
		if !jsonEqual(t, g.InputSchema, d.Schema) {
			t.Errorf("tool %s: the registered schema differs from the contract's", d.Name)
		}
		if d.Name != wantNames[i] {
			t.Errorf("tool %d is %q, want %q", i, d.Name, wantNames[i])
		}
	}
	// The tools of the design note, section 5, and nothing mutating beyond what it says.
	mutating := map[string]bool{ToolEditTimeline: true, ToolMatchExposure: true, ToolRenderPreview: true}
	for name, tool := range e.tools {
		if tool.Mutates() != mutating[name] {
			t.Errorf("%s: Mutates() = %v", name, tool.Mutates())
		}
	}
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(ab, bb)
}

// What the API accepts in the schema of a strict tool (structured outputs): no numeric or string constraints, no maxItems, no
// oneOf and so on. The contract keeps strict tools inside that subset and puts limits in the descriptions.
var strictAllowed = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true, "enum": true, "const": true,
	"description": true, "title": true, "anyOf": true, "allOf": true, "$defs": true, "$ref": true, "default": true,
}

func TestStrictToolsUseOnlyWhatStrictSupports(t *testing.T) {
	var file struct {
		Tools []struct {
			Name        string
			Strict      bool
			InputSchema map[string]any `json:"input_schema"`
		}
	}
	if err := json.Unmarshal(ContractJSON(), &file); err != nil {
		t.Fatal(err)
	}
	for _, tool := range file.Tools {
		if !tool.Strict {
			if tool.Name != ToolEditTimeline {
				t.Errorf("%s is not strict; only edit_timeline may be (its ops use ranges, patterns and oneOf)", tool.Name)
			}
			continue
		}
		var walk func(path string, n any)
		walk = func(path string, n any) {
			switch v := n.(type) {
			case map[string]any:
				for k, child := range v {
					// Property names are free; only the keywords of a schema are checked.
					if path != "" && strings.HasSuffix(path, "/properties") {
						walk(path+"/"+k, child)
						continue
					}
					if !strictAllowed[k] {
						t.Errorf("%s%s: the keyword %q is not supported under strict tool use", tool.Name, path, k)
					}
					walk(path+"/"+k, child)
				}
				if typ, _ := v["type"].(string); typ == "object" {
					if ap, ok := v["additionalProperties"].(bool); !ok || ap {
						t.Errorf("%s%s: an object must set additionalProperties to false", tool.Name, path)
					}
				}
			case []any:
				for i, child := range v {
					walk(path+"/"+string(rune('0'+i%10)), child)
				}
			}
		}
		walk("", tool.InputSchema)
	}
}

// edit_timeline's ops are the op schema, bundled into the tool's own schema.
func TestEditTimelineSchemaCarriesTheOps(t *testing.T) {
	d, err := contractTool(ToolEditTimeline)
	if err != nil {
		t.Fatal(err)
	}
	if d.Strict {
		t.Fatal("edit_timeline must not be strict")
	}
	if bytes.Contains(d.Schema, []byte("contentfabrique.app")) {
		t.Fatal("the bundled schema still refers to the op schema by URL")
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(d.Schema, &doc); err != nil {
		t.Fatal(err)
	}
	var opsDoc struct {
		Defs  map[string]json.RawMessage `json:"$defs"`
		OneOf []json.RawMessage          `json:"oneOf"`
	}
	if err := json.Unmarshal(ops.SchemaJSON(), &opsDoc); err != nil {
		t.Fatal(err)
	}
	for name := range opsDoc.Defs {
		if name == "opBatch" { // dropped on purpose: its items refer to the root of the op schema
			if _, ok := doc.Defs[name]; ok {
				t.Error("opBatch is still in the bundled schema")
			}
			continue
		}
		if _, ok := doc.Defs[name]; !ok {
			t.Errorf("the bundled schema lacks the definition %q of the op schema", name)
		}
	}
	for _, name := range ops.Names {
		if _, ok := doc.Defs[name]; !ok {
			t.Errorf("no definition for the op %q", name)
		}
	}

	sch := compile(t, d.Schema)
	valid := []string{
		`{"base_version":0,"ops":[{"op":"add_clip","asset_id":"ast_01j9z3x7m8c4v6n2q5r0t1y8ab","source":{"in_ms":0,"out_ms":3000}}]}`,
		`{"base_version":4,"ops":[{"op":"set_look","preset":"punchy"},{"op":"add_text","text":"GO","style":"title_bold","at_ms":0,"duration_ms":1500,"anchor":"upper_third"}]}`,
	}
	for _, v := range valid {
		if err := sch.Validate(parseDoc(t, v)); err != nil {
			t.Errorf("valid input refused: %v\n%s", err, v)
		}
	}
	invalid := map[string]string{
		"no ops":            `{"base_version":0}`,
		"no base":           `{"ops":[{"op":"set_look","preset":"bw"}]}`,
		"a fractional base": `{"base_version":1.5,"ops":[{"op":"set_look","preset":"bw"}]}`,
		"empty batch":       `{"base_version":0,"ops":[]}`,
		"unknown op":        `{"base_version":0,"ops":[{"op":"explode"}]}`,
		"extra property":    `{"base_version":0,"ops":[{"op":"set_look","preset":"bw"}],"project_id":"x"}`,
		"bad asset id":      `{"base_version":0,"ops":[{"op":"add_clip","asset_id":"nope","source":{"in_ms":0,"out_ms":3000}}]}`,
		"out of range":      `{"base_version":0,"ops":[{"op":"set_speed","id":"c1","speed":99}]}`,
		"not an array":      `{"base_version":0,"ops":{"op":"set_look","preset":"bw"}}`,
		"too many ops":      `{"base_version":0,"ops":[` + strings.TrimSuffix(strings.Repeat(`{"op":"set_look","preset":"bw"},`, ops.MaxBatchOps+1), ",") + `]}`,
	}
	for name, v := range invalid {
		if err := sch.Validate(parseDoc(t, v)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func compile(t *testing.T, schema []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("tool://t.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("tool://t.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func parseDoc(t *testing.T, s string) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Every other tool's schema accepts its documented arguments and refuses anything else.
func TestToolSchemasAcceptAndRefuse(t *testing.T) {
	asset := "ast_01j9z3x7m8c4v6n2q5r0t1y8ab"
	cases := []struct {
		tool    string
		valid   []string
		invalid []string
	}{
		{ToolGetBrief, []string{`{}`}, []string{`{"x":1}`}},
		{ToolListFootage, []string{`{}`}, []string{`{"x":1}`}},
		{ToolGetTimeline, []string{`{}`}, []string{`[]`}},
		{ToolRenderPreview, []string{`{}`}, []string{`{"size":"big"}`}},
		{ToolViewClip,
			[]string{`{"asset_id":"` + asset + `"}`, `{"asset_id":"` + asset + `","at_ms":[0,1500]}`},
			[]string{`{}`, `{"asset_id":1}`, `{"asset_id":"` + asset + `","at_ms":["1"]}`, `{"asset_id":"` + asset + `","at_ms":[1.5]}`, `{"asset_id":"` + asset + `","extra":1}`}},
		{ToolFindMoments,
			[]string{`{}`, `{"asset_id":"` + asset + `"}`, `{"kind":"rep"}`, `{"kind":"hold","min_score":0.7,"limit":5}`, `{"kind":"any","asset_id":"` + asset + `","min_score":0,"limit":20}`},
			[]string{`{"kind":"pose"}`, `{"asset_id":1}`, `{"min_score":"high"}`, `{"limit":1.5}`, `{"limit":"5"}`, `{"query":"row"}`}},
		{ToolFinish, []string{`{"summary":"Done."}`, `{"summary":"Done.","no_edit_needed":true}`}, []string{`{}`, `{"summary":3}`, `{"summary":"x","open_questions":[]}`, `{"summary":"x","no_edit_needed":"yes"}`}},
	}
	for _, c := range cases {
		d, err := contractTool(c.tool)
		if err != nil {
			t.Fatal(err)
		}
		sch := compile(t, d.Schema)
		for _, v := range c.valid {
			if err := sch.Validate(parseDoc(t, v)); err != nil {
				t.Errorf("%s: %s refused: %v", c.tool, v, err)
			}
		}
		for _, v := range c.invalid {
			if err := sch.Validate(parseDoc(t, v)); err == nil {
				t.Errorf("%s: %s accepted", c.tool, v)
			}
		}
	}
}

// No bundled schema keeps a reference to the root of its document: in the op schema that reference means "an op", and the
// root of a tool schema is the tool's arguments.
func TestBundledSchemasHaveNoSelfReference(t *testing.T) {
	defs, err := Contract()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		for _, bad := range []string{`"$ref":"#"`, `"opBatch"`} {
			if bytes.Contains(d.Schema, []byte(bad)) {
				t.Errorf("%s: the bundled schema contains %s", d.Name, bad)
			}
		}
		var doc any
		if err := json.Unmarshal(d.Schema, &doc); err != nil || selfReference(doc) {
			t.Errorf("%s: self reference %v", d.Name, err)
		}
	}
}

func TestBundle(t *testing.T) {
	opsDoc := []byte(`{"$id":"https://contentfabrique.app/schemas/timeline/ops.v1.json","oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"type":"object","additionalProperties":false}}}`)
	t.Run("no reference: unchanged", func(t *testing.T) {
		out, err := bundle([]byte(`{"type":"object","additionalProperties":false}`), opsDoc)
		if err != nil || string(out) != `{"additionalProperties":false,"type":"object"}` {
			t.Fatalf("%s %v", out, err)
		}
	})
	t.Run("reference replaced, definitions added", func(t *testing.T) {
		out, err := bundle([]byte(`{"type":"object","properties":{"o":{"type":"array","items":{"$ref":"`+opsSchemaID+`"}}},"additionalProperties":false}`), opsDoc)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		if strings.Contains(s, "contentfabrique.app") || !strings.Contains(s, `"$ref":"#/$defs/edit_op"`) || !strings.Contains(s, `"edit_op":{"oneOf"`) || !strings.Contains(s, `"a":{`) {
			t.Fatalf("%s", s)
		}
	})
	t.Run("a clash is refused", func(t *testing.T) {
		_, err := bundle([]byte(`{"type":"object","$defs":{"a":{}},"properties":{"o":{"$ref":"`+opsSchemaID+`"}}}`), opsDoc)
		if err == nil {
			t.Fatal("clash accepted")
		}
	})
	t.Run("opBatch is dropped and a stray root reference is refused", func(t *testing.T) {
		withBatch := []byte(`{"$id":"https://contentfabrique.app/schemas/timeline/ops.v1.json","oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"type":"object"},"opBatch":{"type":"array","items":{"$ref":"#"}}}}`)
		out, err := bundle([]byte(`{"type":"object","properties":{"o":{"$ref":"`+opsSchemaID+`"}}}`), withBatch)
		if err != nil || strings.Contains(string(out), "opBatch") || strings.Contains(string(out), `"$ref":"#"`) {
			t.Fatalf("%s %v", out, err)
		}
		stray := []byte(`{"$id":"https://contentfabrique.app/schemas/timeline/ops.v1.json","oneOf":[{"$ref":"#"}],"$defs":{}}`)
		if _, err := bundle([]byte(`{"type":"object","properties":{"o":{"$ref":"`+opsSchemaID+`"}}}`), stray); err == nil {
			t.Fatal("a reference to the root was left in")
		}
	})
	t.Run("the same input gives the same output", func(t *testing.T) {
		in := []byte(`{"type":"object","properties":{"o":{"$ref":"` + opsSchemaID + `"}},"additionalProperties":false}`)
		a, _ := bundle(in, opsDoc)
		for range 5 {
			if b, _ := bundle(in, opsDoc); !bytes.Equal(a, b) {
				t.Fatal("not deterministic")
			}
		}
	})
}

func TestLoadContractRefusesBadFiles(t *testing.T) {
	for name, file := range map[string]string{
		"not json":      `x`,
		"wrong version": `{"version":"v2","tools":[]}`,
		"no schema":     `{"version":"editor-tools/v1","tools":[{"name":"a","description":"d"}]}`,
		"no desc":       `{"version":"editor-tools/v1","tools":[{"name":"a","input_schema":{"type":"object"}}]}`,
		"duplicate":     `{"version":"editor-tools/v1","tools":[{"name":"a","description":"d","input_schema":{}},{"name":"a","description":"d","input_schema":{}}]}`,
	} {
		if _, err := loadContract([]byte(file), ops.SchemaJSON()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A tool the registry cannot take (an open object under strict) fails at registration, not in front of the model.
func TestNewAgentChecksItsTools(t *testing.T) {
	e := newEnv(t)
	a, err := NewAgent(e.deps)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(a.Tools))
	for i, tl := range a.Tools {
		names[i] = tl.Name()
	}
	sorted := slices.Clone(names)
	sort.Strings(sorted)
	if len(names) != 13 {
		t.Fatalf("tools: %v", names)
	}
	if _, err := runtime.NewAgents(a); err != nil {
		t.Fatal(err)
	}
	if a.Name != "editor" || a.Stage != "editor" || a.Budgets.Micros != 5_000_000 || a.Budgets.ToolCalls != 60 || a.Budgets.WallClock.Minutes() != 15 {
		t.Fatalf("agent: %+v", a)
	}
	if _, err := NewAgent(Deps{}); err == nil {
		t.Fatal("an agent without services was built")
	}
}
