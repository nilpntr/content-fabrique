package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The payloads of the events. The conversation is rebuilt from them on every start, so they hold everything the model
// needs to see again: the content of each assistant message (thinking blocks with their signatures included) and of each
// tool result. Images are references to storage, never bytes.

// wireBlock is a content block as stored in an event.
type wireBlock struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	// An image, by reference.
	Ref       string `json:"ref,omitempty"`
	MediaType string `json:"media_type,omitempty"`

	// A tool_use.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// A tool_result.
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   []wireBlock `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`

	// Thinking.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`

	// A block the llm package does not model.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// toWire converts blocks for storage. An image with bytes and no reference is refused: bytes do not belong in an event.
func toWire(blocks []llm.Block) ([]wireBlock, error) {
	out := make([]wireBlock, 0, len(blocks))
	for _, b := range blocks {
		w := wireBlock{Type: string(b.Type)}
		switch b.Type {
		case llm.BlockText:
			w.Text = b.Text
		case llm.BlockImage:
			if b.Image == nil || b.Image.Ref == "" {
				return nil, errors.New("an image must be a reference to storage (Image.Ref), not bytes")
			}
			w.Ref, w.MediaType = b.Image.Ref, b.Image.MediaType
		case llm.BlockToolUse:
			w.ID, w.Name, w.Input = b.ID, b.Name, b.Input
			if len(w.Input) == 0 {
				w.Input = json.RawMessage("{}")
			}
		case llm.BlockToolResult:
			inner, err := toWire(b.Content)
			if err != nil {
				return nil, err
			}
			w.ToolUseID, w.Content, w.IsError = b.ToolUseID, inner, b.IsError
		case llm.BlockThinking:
			w.Thinking, w.Signature = b.Thinking, b.Signature
		case llm.BlockRedactedThinking:
			w.Data = b.Data
		default:
			if len(b.Raw) == 0 {
				return nil, fmt.Errorf("unknown block type %q", b.Type)
			}
			w.Raw = b.Raw
		}
		out = append(out, w)
	}
	return out, nil
}

// fromWire is the inverse of toWire.
func fromWire(blocks []wireBlock) []llm.Block {
	out := make([]llm.Block, 0, len(blocks))
	for _, w := range blocks {
		b := llm.Block{Type: llm.BlockType(w.Type)}
		switch b.Type {
		case llm.BlockText:
			b.Text = w.Text
		case llm.BlockImage:
			b.Image = &llm.Image{MediaType: w.MediaType, Ref: w.Ref}
		case llm.BlockToolUse:
			b.ID, b.Name, b.Input = w.ID, w.Name, w.Input
		case llm.BlockToolResult:
			b.ToolUseID, b.Content, b.IsError = w.ToolUseID, fromWire(w.Content), w.IsError
		case llm.BlockThinking:
			b.Thinking, b.Signature = w.Thinking, w.Signature
		case llm.BlockRedactedThinking:
			b.Data = w.Data
		default:
			b.Raw = w.Raw
		}
		out = append(out, b)
	}
	return out
}

// usagePayload is a call's usage as stored.
type usagePayload struct {
	InputTokens      int64 `json:"input_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

// The states of a status event.
const (
	stateRunning      = "running"
	stateContext      = "context"
	stateWaitingInput = "waiting_input"
	stateSucceeded    = "succeeded"
	stateFailed       = "failed"
	stateCancelled    = "cancelled"
	stateCostFixed    = "cost_reconciled"
	stateModelCall    = "model_call"
	stateNudge        = "nudge"
)

// statusPayload is a status event.
type statusPayload struct {
	State string `json:"state"`
	// running
	Epoch   int64 `json:"epoch,omitempty"`
	Resumed bool  `json:"resumed,omitempty"`
	// context: the run context blocks, stored once so a resumed run sends the same prefix.
	Blocks []wireBlock `json:"blocks,omitempty"`
	// waiting_input: the unanswered tool call, which tool it is, and the question.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Question  string `json:"question,omitempty"`
	// failed
	Reason string `json:"reason,omitempty"`
	// failed with reason budget: which limit.
	Limit string `json:"limit,omitempty"`
	// cost_reconciled: what a takeover charged that the run's totals did not have (a call that was in flight, or that the
	// ledger holds and the events do not).
	AddedCostMicros int64 `json:"added_cost_micros,omitempty"`
	// nudge: the message the agent sent after a turn that ended without a tool call (Blocks holds it).
	// model_call: written before a request is sent, with the worst-case cost reserved for it.
	ReservedMicros int64 `json:"reserved_micros,omitempty"`
}

// assistantPayload is an assistant_message event.
type assistantPayload struct {
	Content    []wireBlock  `json:"content"`
	StopReason string       `json:"stop_reason"`
	Model      string       `json:"model"`
	RequestID  string       `json:"request_id,omitempty"`
	Usage      usagePayload `json:"usage"`
	CostMicros int64        `json:"cost_micros"`
}

// toolCallPayload is a tool_call event: written before the tool runs.
type toolCallPayload struct {
	ToolUseID string          `json:"tool_use_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Mutates   bool            `json:"mutates,omitempty"`
}

// toolResultPayload is a tool_result event.
type toolResultPayload struct {
	ToolUseID string      `json:"tool_use_id"`
	Name      string      `json:"name"`
	Content   []wireBlock `json:"content"`
	IsError   bool        `json:"is_error,omitempty"`
	Final     bool        `json:"final,omitempty"`
}

// errorPayload is an error event.
type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// mustJSON encodes an event payload. HTML escaping is off: it would turn every < > & into a six-byte escape, and the
// untrusted-data blocks are full of them.
func mustJSON(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("runtime: marshal event payload: %v", err)) // the payload types above always marshal
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// turn is one model turn of the conversation: the assistant's message and the answers to its tool calls.
type turn struct {
	assistant  llm.Message
	stopReason string
	uses       []llm.Block
	nudge      []llm.Block     // the user message that followed a turn with no tool call, if the agent sent one
	called     map[string]bool // tool_call events seen, by tool_use id
	results    map[string]toolResultPayload
}

// pending returns the tool calls without a result, in the order the model made them.
func (t *turn) pending() []llm.Block {
	var out []llm.Block
	for _, u := range t.uses {
		if _, ok := t.results[u.ID]; !ok {
			out = append(out, u)
		}
	}
	return out
}

// complete says every tool call has its result.
func (t *turn) complete() bool { return len(t.pending()) == 0 }

func (t *turn) has(toolUseID string) bool {
	for _, u := range t.uses {
		if u.ID == toolUseID {
			return true
		}
	}
	return false
}

// convo is the conversation rebuilt from a run's events, and kept up to date in memory as the loop appends more.
type convo struct {
	// context is the stored run context; hasContext says whether the event exists (a context can be empty).
	context    []llm.Block
	hasContext bool
	turns      []*turn
	// started says the run has a status event of state running already: this start is a resume.
	started bool
	// nudges counts the nudge events: how often the agent asked the model to go on after a turn that ended without a tool call.
	nudges int
	// inflight is the worst-case cost reserved for a model call that was sent and has no answer on record: the worker died
	// (or was stopped) during it. A takeover charges it to the run, because the call may have been billed.
	inflight int64
}

// rebuild reads the events in order. It returns an error for a log that cannot be a run of this runtime (a tool result for
// a call nobody made, a new assistant message while the previous one still had unanswered calls).
func rebuild(events []store.AgentEvent) (*convo, error) {
	c := &convo{}
	for _, e := range events {
		if err := c.apply(e); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// apply adds one event, in order.
func (c *convo) apply(e store.AgentEvent) error {
	switch e.Type {
	case store.EventStatus:
		var p statusPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("event %d: status payload: %w", e.Seq, err)
		}
		switch p.State {
		case stateContext:
			c.context, c.hasContext = fromWire(p.Blocks), true
		case stateRunning:
			c.started = true
		case stateNudge:
			if cur := c.last(); cur != nil {
				cur.nudge = fromWire(p.Blocks)
				c.nudges++
			}
		case stateModelCall:
			c.inflight = p.ReservedMicros
		case stateCostFixed:
			c.inflight = 0
		}
	case store.EventError:
		var p errorPayload
		if err := json.Unmarshal(e.Payload, &p); err == nil && p.Code != "panic" {
			c.inflight = 0 // the failed call was recorded, with what it cost
		}
	case store.EventAssistantMessage:
		var p assistantPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("event %d: assistant payload: %w", e.Seq, err)
		}
		c.inflight = 0
		if cur := c.last(); cur != nil && !cur.complete() {
			return fmt.Errorf("event %d: a new assistant message while %d tool calls of the last one are unanswered", e.Seq, len(cur.pending()))
		}
		blocks := fromWire(p.Content)
		t := &turn{
			assistant: llm.Message{Role: llm.RoleAssistant, Content: blocks}, stopReason: p.StopReason,
			called: map[string]bool{}, results: map[string]toolResultPayload{},
		}
		for _, b := range blocks {
			if b.Type == llm.BlockToolUse {
				t.uses = append(t.uses, b)
			}
		}
		c.turns = append(c.turns, t)
	case store.EventToolCall:
		var p toolCallPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("event %d: tool_call payload: %w", e.Seq, err)
		}
		cur := c.last()
		if cur == nil || !cur.has(p.ToolUseID) {
			return fmt.Errorf("event %d: tool_call %s belongs to no assistant message", e.Seq, p.ToolUseID)
		}
		cur.called[p.ToolUseID] = true
	case store.EventToolResult:
		var p toolResultPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("event %d: tool_result payload: %w", e.Seq, err)
		}
		cur := c.last()
		if cur == nil || !cur.has(p.ToolUseID) {
			return fmt.Errorf("event %d: tool_result %s belongs to no tool call", e.Seq, p.ToolUseID)
		}
		cur.results[p.ToolUseID] = p
	}
	return nil
}

// last returns the newest turn, or nil.
func (c *convo) last() *turn {
	if len(c.turns) == 0 {
		return nil
	}
	return c.turns[len(c.turns)-1]
}

// lastText is the text of the newest assistant message, for a summary.
func (c *convo) lastText() string {
	for i := len(c.turns) - 1; i >= 0; i-- {
		var s string
		for _, b := range c.turns[i].assistant.Content {
			if b.Type == llm.BlockText {
				s += b.Text
			}
		}
		if s != "" {
			return s
		}
	}
	return ""
}

// messages builds the request's messages: the opening user message (the stored run context, then the opening blocks), and
// for each turn the assistant message and, when every one of its tool calls has a result, the user message that carries
// the results in the order of the calls.
func (c *convo) messages(opening []llm.Block) []llm.Message {
	first := make([]llm.Block, 0, len(c.context)+len(opening))
	first = append(first, c.context...)
	first = append(first, opening...)
	msgs := []llm.Message{{Role: llm.RoleUser, Content: first}}
	for _, t := range c.turns {
		msgs = append(msgs, t.assistant)
		if len(t.uses) == 0 && len(t.nudge) > 0 {
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: t.nudge})
			continue
		}
		if len(t.uses) == 0 || !t.complete() {
			continue
		}
		results := make([]llm.Block, 0, len(t.uses))
		for _, u := range t.uses {
			r := t.results[u.ID]
			results = append(results, llm.Block{Type: llm.BlockToolResult, ToolUseID: u.ID, Content: fromWire(r.Content), IsError: r.IsError})
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: results})
	}
	return msgs
}
