package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

func proposalCall(id string, p map[string]any) llm.Response {
	raw, _ := json.Marshal(p)
	return llm.Response{StopReason: "tool_use", Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: ImproveToolName, Input: raw}}}
}

// stub is what the first live run got back from Opus 5.5: "x" for the title and the path.
func stub() map[string]any {
	return map[string]any{"title": "x", "rationale": "x", "expected_effect": "x", "edits": []map[string]string{{"path": "x", "old": "x", "new": "x"}}, "no_change_reason": ""}
}

func improverRoot(t *testing.T) (string, []SourceFile) {
	t.Helper()
	root := t.TempDir()
	path := "apps/server/internal/agents/editor/pace.go"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte("const (\n\tlongShotMs = 4000\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, []SourceFile{{Path: path, Kind: KindCheckThreshold, Content: "const (\n\tlongShotMs = 4000\n)\n"}}
}

func valid() map[string]any {
	return map[string]any{"title": "Let long takes run to five seconds", "rationale": "Both low cases fail long_shot on takes the creator holds for four to five seconds in his own videos.",
		"expected_effect": "long_shot passes on the long-take cases; nothing else moves",
		"edits":           []map[string]string{{"path": "apps/server/internal/agents/editor/pace.go", "old": "longShotMs = 4000", "new": "longShotMs = 5000"}}, "no_change_reason": ""}
}

// A stub ("x") is refused and sent back with the exact error in the same conversation; the corrected proposal is taken. Both calls share
// the one budget.
func TestImproverAsksAgainAfterARefusedProposal(t *testing.T) {
	root, files := improverRoot(t)
	replies := []llm.Response{proposalCall("toolu_1", stub()), proposalCall("toolu_2", valid())}
	fc := &fakeClient{reply: func(llm.Request) (llm.Response, error) { r := replies[0]; replies = replies[1:]; return r, nil }}
	im := &Improver{Models: fakeFactory{fc}, Router: fakeRouter{}}
	validate := func(p Proposal) (Proposal, error) { return ValidateProposal(p, root) }
	p, cost, attempts, err := im.Propose(context.Background(), ids.WorkspaceID{UUID: uuid.New()}, ImproverInput{Files: files}, 300_000, validate, MaxImproverAttempts)
	if err != nil || p.Title != "Let long takes run to five seconds" || p.Kind != KindCheckThreshold {
		t.Fatalf("proposal %+v err %v", p, err)
	}
	if len(attempts) != 2 || !strings.Contains(attempts[0].Error, `the title "x" is not a description of a change`) || attempts[1].Error != "" {
		t.Errorf("attempts %+v", attempts)
	}
	if len(fc.calls) != 2 || cost != 2*12_345 {
		t.Errorf("%d calls, cost %d", len(fc.calls), cost)
	}
	// The second request carries the stub and a tool result with the error.
	msgs := fc.calls[1].Messages
	last := msgs[len(msgs)-1].Content[0]
	if len(msgs) != 3 || msgs[1].Role != llm.RoleAssistant || last.Type != llm.BlockToolResult || last.ToolUseID != "toolu_1" || !last.IsError ||
		!strings.Contains(last.Content[0].Text, "Refused: the title") || !strings.Contains(last.Content[0].Text, "attempt 2 of 3") {
		t.Errorf("the retry turn: %+v", msgs)
	}
}

// After three refused proposals the iteration fails with the last error; a proposal that says there is no change worth making ends it
// without retries.
func TestImproverGivesUpAndNoChange(t *testing.T) {
	root, files := improverRoot(t)
	badPath := valid()
	badPath["edits"] = []map[string]string{{"path": "apps/server/go.mod", "old": "module x", "new": "module y"}}
	notFound := valid()
	notFound["edits"] = []map[string]string{{"path": "apps/server/internal/agents/editor/pace.go", "old": "longShotMs = 9999", "new": "longShotMs = 5000"}}
	replies := []llm.Response{proposalCall("t1", stub()), proposalCall("t2", badPath), proposalCall("t3", notFound)}
	fc := &fakeClient{reply: func(llm.Request) (llm.Response, error) { r := replies[0]; replies = replies[1:]; return r, nil }}
	im := &Improver{Models: fakeFactory{fc}, Router: fakeRouter{}}
	validate := func(p Proposal) (Proposal, error) { return ValidateProposal(p, root) }
	_, _, attempts, err := im.Propose(context.Background(), ids.WorkspaceID{UUID: uuid.New()}, ImproverInput{Files: files}, 300_000, validate, MaxImproverAttempts)
	if err == nil || !strings.Contains(err.Error(), "3 proposals were all refused") || !strings.Contains(err.Error(), "occurs 0 times") {
		t.Errorf("err %v", err)
	}
	if len(attempts) != 3 || !strings.Contains(attempts[1].Error, "not on the allowlist") {
		t.Errorf("attempts %+v", attempts)
	}

	none := map[string]any{"title": "", "rationale": "", "expected_effect": "", "edits": []map[string]string{}, "no_change_reason": "every low case fails on footage, not on a rule"}
	fc = &fakeClient{reply: func(llm.Request) (llm.Response, error) { return proposalCall("t1", none), nil }}
	im = &Improver{Models: fakeFactory{fc}, Router: fakeRouter{}}
	_, _, attempts, err = im.Propose(context.Background(), ids.WorkspaceID{UUID: uuid.New()}, ImproverInput{Files: files}, 300_000, validate, MaxImproverAttempts)
	if !errors.Is(err, ErrNoChange) || len(fc.calls) != 1 || len(attempts) != 1 {
		t.Errorf("no change: %v, %d calls", err, len(fc.calls))
	}
}

// The schema only admits the files shown as a path (strict tool use takes enum); the files are our own source, not untrusted data, with a
// cache breakpoint so a retry reads them from the cache.
func TestImproverSchemaAndFiles(t *testing.T) {
	_, files := improverRoot(t)
	req := BuildImproveRequest(ImproverInput{Files: files})
	var schema map[string]any
	if err := json.Unmarshal(req.Tools[0].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	get := func(m any, k string) any { mm, _ := m.(map[string]any); return mm[k] }
	path, _ := get(get(get(get(get(schema, "properties"), "edits"), "items"), "properties"), "path").(map[string]any)
	enum, _ := path["enum"].([]any)
	if len(enum) != 1 || enum[0] != files[0].Path {
		t.Errorf("path enum %v", path)
	}
	req0 := req.Messages[0].Content[0]
	if !strings.HasPrefix(req0.Text, `<file path="apps/server/internal/agents/editor/pace.go" kind="check_threshold">`) || req0.Cache == nil {
		t.Errorf("file block %q cache %v", req0.Text[:60], req0.Cache)
	}
	for _, bad := range []Proposal{
		{Title: "x", Rationale: strings.Repeat("r", 50), ExpectedEffect: "e", Edits: []Edit{{Path: "p", Old: "old text", New: "n"}}},
		{Title: "A real title here", Rationale: "short", ExpectedEffect: "e", Edits: []Edit{{Path: "p", Old: "old text", New: "n"}}},
		{Title: "A real title here", Rationale: strings.Repeat("r", 50), ExpectedEffect: "e"},
		{Title: "A real title here", Rationale: strings.Repeat("r", 50), ExpectedEffect: "e", Edits: []Edit{{Path: "x", Old: "old text", New: "n"}}},
		{Title: "A real title here", Rationale: strings.Repeat("r", 50), ExpectedEffect: "e", Edits: []Edit{{Path: "p", Old: "x", New: "n"}}},
	} {
		if err := CheckProposalText(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
