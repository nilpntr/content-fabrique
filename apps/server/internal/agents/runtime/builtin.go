package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The tools every agent can use, and the echo agent the tests and the dev CLI run.

const finishSchema = `{
  "type": "object",
  "properties": {"summary": {"type": "string", "description": "What was done, in a sentence or two."}},
  "required": ["summary"],
  "additionalProperties": false
}`

// FinishTool is the tool that ends a run: finish(summary). Its result is Final, and the run's output is {"summary": ...}.
func FinishTool() Tool {
	return NewTool(Spec{
		Name:        "finish",
		Description: "End the task. Call it once the work is done, with a short summary of what you did. Nothing can be done after it.",
		Schema:      json.RawMessage(finishSchema),
	}, func(_ context.Context, _ RunContext, in struct {
		Summary string `json:"summary"`
	}) (Result, error) {
		if strings.TrimSpace(in.Summary) == "" {
			return Errorf("the summary is empty; say what was done"), nil
		}
		out, _ := json.Marshal(map[string]string{"summary": in.Summary})
		res := Text(in.Summary)
		res.Final, res.Output = true, out
		return res, nil
	})
}

const askUserSchema = `{
  "type": "object",
  "properties": {"question": {"type": "string", "description": "One clear question for the user."}},
  "required": ["question"],
  "additionalProperties": false
}`

// AskUserTool is the tool that pauses a run for the user's answer: ask_user(question). The run moves to waiting_input; the
// answer arrives, through Service.Reply, as the tool's result.
func AskUserTool() Tool {
	return NewTool(Spec{
		Name:        "ask_user",
		Description: "Ask the user one question when you cannot go on without their answer. The run pauses until they reply; use it sparingly.",
		Schema:      json.RawMessage(askUserSchema),
	}, func(_ context.Context, _ RunContext, in struct {
		Question string `json:"question"`
	}) (Result, error) {
		if strings.TrimSpace(in.Question) == "" {
			return Errorf("the question is empty"), nil
		}
		res := Text(in.Question)
		res.Ask = true
		return res, nil
	})
}

// NoteSink is where the echo agent's note tool makes its effect. Record must be idempotent in key: a call with a key it has
// seen changes nothing and returns applied false.
type NoteSink interface {
	Record(ctx context.Context, key, text string) (applied bool, err error)
}

const noteSchema = `{
  "type": "object",
  "properties": {"text": {"type": "string", "description": "The note to record."}},
  "required": ["text"],
  "additionalProperties": false
}`

// EchoSystem is the echo agent's system prompt.
const EchoSystem = `You are the echo test agent. The user's message arrives in an untrusted-data block.
1. Call the note tool exactly once, with the user's message as the text.
2. Then call the finish tool with a one-sentence summary that repeats the note.
Do not answer in plain text.`

// NewEchoAgent defines the echo agent: a tiny agent for tests and for measuring the runtime. It has two tools, note(text),
// which changes state (and so gets an idempotency key), and finish(summary). sink may be nil: the note is then only part of
// the run's events. The stage is ideation, which the routing profiles map to a model.
func NewEchoAgent(sink NoteSink) Agent {
	note := NewTool(Spec{
		Name:        "note",
		Description: "Record a note. Changes state: call it once per message.",
		Schema:      json.RawMessage(noteSchema),
		Mutates:     true,
	}, func(ctx context.Context, rc RunContext, in struct {
		Text string `json:"text"`
	}) (Result, error) {
		if strings.TrimSpace(in.Text) == "" {
			return Errorf("the text is empty"), nil
		}
		if sink == nil {
			return Text("noted"), nil
		}
		if rc.IdempotencyKey == "" {
			return Result{}, errors.New("note: no idempotency key")
		}
		applied, err := sink.Record(ctx, rc.IdempotencyKey, in.Text)
		if err != nil {
			return Result{}, err
		}
		if !applied {
			return Text("noted (already recorded)"), nil
		}
		return Text("noted"), nil
	})
	return Agent{
		Name:    "echo",
		Stage:   "ideation",
		System:  EchoSystem,
		Tools:   []Tool{note, FinishTool()},
		Budgets: Budgets{Micros: 100_000, Tokens: 100_000, ToolCalls: 10, WallClock: 5 * time.Minute},
	}
}
