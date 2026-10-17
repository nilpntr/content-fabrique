package editor

import (
	"bytes"
	_ "embed" // the contract is embedded
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// The tool contract is packages/schemas/agents/editor-tools.v1.json, the source of truth; this is an embedded copy. `task gen`
// (or `go generate ./internal/agents/...`) refreshes it and CI fails if it differs.
//
//go:generate cp ../../../../../packages/schemas/agents/editor-tools.v1.json contract/editor-tools.v1.json
//go:embed contract/editor-tools.v1.json
var contractJSON []byte

// ContractJSON returns the embedded contract file.
func ContractJSON() []byte { return bytes.Clone(contractJSON) }

// opsSchemaID is the $id of the edit-op schema: where the contract's edit_timeline refers to it.
const opsSchemaID = "https://contentfabrique.app/schemas/timeline/ops.v1.json"

// opDef is the name the bundled schema gives the edit op (the root of the op schema) among its $defs.
const opDef = "edit_op"

// ToolDef is one tool of the contract as the agent registers it: the schema is bundled, that is, self-contained.
type ToolDef struct {
	Name        string
	Description string
	// Strict says whether the tool is sent with strict tool use.
	Strict bool
	// Schema is the input schema with every reference resolved inside the document.
	Schema json.RawMessage
}

type contractFile struct {
	Version string `json:"version"`
	Tools   []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Strict      bool            `json:"strict"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
}

// loadContract parses a contract file and bundles each tool's schema with opsSchema (the op schema's JSON).
func loadContract(file, opsSchema []byte) ([]ToolDef, error) {
	var c contractFile
	if err := json.Unmarshal(file, &c); err != nil {
		return nil, fmt.Errorf("editor tools contract: %w", err)
	}
	if c.Version != "editor-tools/v1" {
		return nil, fmt.Errorf("editor tools contract: version %q is not editor-tools/v1", c.Version)
	}
	out := make([]ToolDef, 0, len(c.Tools))
	seen := map[string]bool{}
	for _, t := range c.Tools {
		if t.Name == "" || t.Description == "" || len(t.InputSchema) == 0 {
			return nil, fmt.Errorf("editor tools contract: tool %q needs a name, a description and an input_schema", t.Name)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("editor tools contract: tool %q is listed twice", t.Name)
		}
		seen[t.Name] = true
		schema, err := bundle(t.InputSchema, opsSchema)
		if err != nil {
			return nil, fmt.Errorf("editor tools contract: tool %q: %w", t.Name, err)
		}
		out = append(out, ToolDef{Name: t.Name, Description: t.Description, Strict: t.Strict, Schema: schema})
	}
	return out, nil
}

// contract is the embedded contract, loaded once.
var contract = sync.OnceValues(func() ([]ToolDef, error) { return loadContract(contractJSON, ops.SchemaJSON()) })

// Contract returns the tools of the embedded contract, in the file's order.
func Contract() ([]ToolDef, error) {
	defs, err := contract()
	if err != nil {
		return nil, err
	}
	return append([]ToolDef(nil), defs...), nil
}

func contractTool(name string) (ToolDef, error) {
	defs, err := contract()
	if err != nil {
		return ToolDef{}, err
	}
	for _, d := range defs {
		if d.Name == name {
			return d, nil
		}
	}
	return ToolDef{}, fmt.Errorf("editor tools contract: there is no tool %q", name)
}

// bundle makes a tool schema self-contained. The contract points at the op schema by its $id; the API gets one document, so
// each such reference becomes a reference to a definition of the schema itself, and the op schema's own definitions are added
// under $defs (their references are all relative to the document root, so they keep working there). A schema without the
// reference comes back unchanged apart from its encoding. The result is deterministic.
func bundle(schema, opsSchema json.RawMessage) (json.RawMessage, error) {
	root, err := decode(schema)
	if err != nil {
		return nil, err
	}
	used := false
	root = replaceRef(root, &used)
	if used {
		obj, ok := root.(map[string]any)
		if !ok {
			return nil, errors.New("the schema is not an object")
		}
		opsDoc, err := decode(opsSchema)
		if err != nil {
			return nil, fmt.Errorf("the op schema: %w", err)
		}
		opsObj, ok := opsDoc.(map[string]any)
		if !ok {
			return nil, errors.New("the op schema is not an object")
		}
		defs, _ := obj["$defs"].(map[string]any)
		if defs == nil {
			defs = map[string]any{}
		}
		opDefs, _ := opsObj["$defs"].(map[string]any)
		for k, v := range opDefs {
			if _, clash := defs[k]; clash {
				return nil, fmt.Errorf("$defs/%s is defined by the tool and by the op schema", k)
			}
			defs[k] = v
		}
		if _, clash := defs[opDef]; clash {
			return nil, fmt.Errorf("$defs/%s is defined twice", opDef)
		}
		edit := map[string]any{"oneOf": opsObj["oneOf"]}
		for _, k := range []string{"title", "description"} {
			if v, ok := opsObj[k]; ok {
				edit[k] = v
			}
		}
		defs[opDef] = edit
		obj["$defs"] = defs
	}
	return json.Marshal(root) // map keys are sorted: deterministic
}

func decode(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// replaceRef rewrites {"$ref": opsSchemaID} nodes to {"$ref": "#/$defs/edit_op"} and records that it did.
func replaceRef(n any, used *bool) any {
	switch v := n.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && ref == opsSchemaID {
			*used = true
			out := map[string]any{"$ref": "#/$defs/" + opDef}
			for k, val := range v { // keep title/description written next to the reference
				if k != "$ref" {
					out[k] = replaceRef(val, used)
				}
			}
			return out
		}
		for k, val := range v {
			v[k] = replaceRef(val, used)
		}
		return v
	case []any:
		for i, val := range v {
			v[i] = replaceRef(val, used)
		}
		return v
	}
	return n
}
