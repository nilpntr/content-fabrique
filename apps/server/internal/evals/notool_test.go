package evals

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

// Sonnet 5.5 cannot be forced to call score_edit: an answer in text is asked again once (inside the same budget), and a second text
// answer is a verdict without scores ("no_answer"), not an error.
func TestJudgeAsksAgainWhenTheModelAnswersInText(t *testing.T) {
	textReply := llm.Response{Model: "claude-sonnet-5-5", StopReason: "end_turn", Content: []llm.Block{llm.TextBlock("hook 4, energy 3")}}
	for _, c := range []struct {
		name    string
		replies []func(llm.Request) (llm.Response, error)
		calls   int
		reason  string
	}{
		{"text then the tool", []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) { return textReply, nil }, toolReply(goodJudgement("posing-hook"))}, 2, ""},
		{"text twice", []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) { return textReply, nil }, func(llm.Request) (llm.Response, error) { return textReply, nil }}, 2, "no_answer"},
	} {
		n := 0
		fc := &fakeClient{reply: func(r llm.Request) (llm.Response, error) { n++; return c.replies[n-1](r) }}
		j := &Judge{Models: fakeFactory{fc}, Router: fakeRouter{}}
		v, err := j.Score(context.Background(), ids.WorkspaceID{UUID: uuid.New()}, uuid.New(), judgeIn(t), 500_000)
		if err != nil || v.Reason != c.reason || len(fc.calls) != c.calls || v.CostMicros != int64(c.calls)*12_345 {
			t.Errorf("%s: verdict %+v err %v calls %d", c.name, v, err, len(fc.calls))
		}
		if c.reason == "" && len(v.Scores) != 6 {
			t.Errorf("%s: scores %v", c.name, v.Scores)
		}
	}
}
