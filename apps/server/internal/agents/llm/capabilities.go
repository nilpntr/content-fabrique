package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
)

// What each model accepts (config/routing/models.yaml, docs/design/agent-runtime-v1.md section 2): how thinking is turned off, at which
// efforts, and whether a tool call can be forced. Callers write one request (ThinkingDisabled, a forced ToolChoice) and the Anthropic
// client maps it to what the model takes, so a call site does not need to know the model; a model the catalogue does not describe is
// refused before anything is sent.

// How a model turns thinking off.
const (
	ThinkingOffDisabled     = "disabled"
	ThinkingOffBetweenTools = "between_tools"
	ThinkingOffOmit         = "omit"
)

// ThinkingBetweenTools is the thinking mode the client sends for ThinkingDisabled on a model whose thinking_off is between_tools. Callers
// ask for ThinkingDisabled; this mode is the wire form.
const ThinkingBetweenTools ThinkingMode = "between_tools"

// Capability is what one model accepts.
type Capability struct {
	ThinkingOff          string
	ThinkingOffMaxEffort string
	ForcedToolChoice     bool
}

// Capabilities are the capabilities by model id.
type Capabilities map[string]Capability

// efforts are the effort levels, lowest first.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// CapabilitiesFrom checks the catalogue's capabilities: every model says how it turns thinking off and whether a tool call can be forced.
func CapabilitiesFrom(cat routing.Catalog) (Capabilities, error) {
	out := Capabilities{}
	var errs []error
	for id, m := range cat {
		switch m.ThinkingOff {
		case ThinkingOffDisabled, ThinkingOffBetweenTools, ThinkingOffOmit:
		default:
			errs = append(errs, fmt.Errorf("model %s: thinking_off %q is not disabled, between_tools or omit", id, m.ThinkingOff))
		}
		if m.ThinkingOffMaxEffort != "" && !slices.Contains(efforts, m.ThinkingOffMaxEffort) {
			errs = append(errs, fmt.Errorf("model %s: thinking_off_max_effort %q is not an effort", id, m.ThinkingOffMaxEffort))
		}
		if m.ForcedToolChoice == nil {
			errs = append(errs, fmt.Errorf("model %s: forced_tool_choice is not set", id))
			continue
		}
		out[id] = Capability{ThinkingOff: m.ThinkingOff, ThinkingOffMaxEffort: m.ThinkingOffMaxEffort, ForcedToolChoice: *m.ForcedToolChoice}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("llm: %s: %w", routing.CatalogFile, errors.Join(errs...))
	}
	return out, nil
}

var (
	defaultCapsOnce sync.Once
	defaultCaps     Capabilities
	defaultCapsErr  error
)

// DefaultCapabilities are the capabilities of the catalogue this build embeds.
func DefaultCapabilities() (Capabilities, error) {
	defaultCapsOnce.Do(func() {
		cat, err := routing.LoadCatalog(configfiles.Routing())
		if err != nil {
			defaultCapsErr = err
			return
		}
		defaultCaps, defaultCapsErr = CapabilitiesFrom(cat)
	})
	return defaultCaps, defaultCapsErr
}

// UnsupportedError says a request asks for something its model does not accept, before anything was sent.
type UnsupportedError struct {
	Model  string
	Reason string
}

func (e *UnsupportedError) Error() string { return "llm: " + e.Model + ": " + e.Reason }

// Is makes errors.Is(err, ErrUnsupported) true.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// ErrUnsupported is wrapped by every *UnsupportedError.
var ErrUnsupported = errors.New("llm: the model does not accept the request")

// adapt maps a request to what its model accepts: ThinkingDisabled becomes the model's way of turning thinking off (and is refused above
// its effort limit), and a forced tool choice becomes auto, one call at a time, with the named tool strict, on a model that does not take
// forcing. A model the capabilities do not list is refused.
func (caps Capabilities) adapt(req Request) (Request, error) {
	c, ok := caps[req.Model]
	if !ok {
		return req, &UnsupportedError{Model: req.Model, Reason: "the model is not in config/routing/models.yaml, so how to send thinking and tool choice to it is not known; add it there"}
	}
	if t := req.Thinking; t != nil && t.Mode == ThinkingDisabled {
		if c.ThinkingOffMaxEffort != "" && req.Effort != "" && slices.Index(efforts, req.Effort) > slices.Index(efforts, c.ThinkingOffMaxEffort) {
			return req, &UnsupportedError{Model: req.Model, Reason: fmt.Sprintf("thinking can be turned off only at effort %s or below, and the request asks for %s; lower the effort in the routing entry", c.ThinkingOffMaxEffort, req.Effort)}
		}
		switch c.ThinkingOff {
		case ThinkingOffDisabled:
		case ThinkingOffBetweenTools:
			req.Thinking = &Thinking{Mode: ThinkingBetweenTools} // no other field: display or a budget is a 400
		case ThinkingOffOmit:
			req.Thinking = nil
		}
	}
	if tc := req.ToolChoice; tc != nil && (tc.Type == "any" || tc.Type == "tool") && !c.ForcedToolChoice {
		if tc.Type == "tool" {
			found := false
			tools := make([]Tool, len(req.Tools))
			for i, t := range req.Tools {
				if t.Name == tc.Name {
					t.Strict, found = true, true
				}
				tools[i] = t
			}
			if !found {
				return req, &UnsupportedError{Model: req.Model, Reason: fmt.Sprintf("tool_choice names %q, which is not one of the request's tools", tc.Name)}
			}
			req.Tools = tools
		}
		req.ToolChoice = &ToolChoice{Type: "auto", DisableParallelToolUse: true}
	}
	return req, nil
}

// ---- a call that must end in one tool call ------------------------------------------------------------------------------------

// NoToolCallError says the model answered twice without calling the tool the request asked for.
type NoToolCallError struct {
	Tool       string
	StopReason string
}

func (e *NoToolCallError) Error() string {
	return fmt.Sprintf("llm: the model did not call %s (it stopped with %s, after a reminder)", e.Tool, e.StopReason)
}

// Is makes errors.Is(err, ErrNoToolCall) true.
func (e *NoToolCallError) Is(target error) bool { return target == ErrNoToolCall }

// ErrNoToolCall is wrapped by every *NoToolCallError.
var ErrNoToolCall = errors.New("llm: the model did not call the tool")

// toolReminder is the message that asks again.
const toolReminder = "Your answer did not call the %s tool. Call %s now, once, with your answer as its input; do not answer in text."

// CallTool sends a request that asks for one call of the tool named (its ToolChoice names the tool) and returns the response and that
// call. On a model that cannot be forced the tool choice is auto (see adapt), so the model may answer in text instead: CallTool then asks
// once more, with the answer and a reminder appended, and returns a *NoToolCallError when the second answer has no call either. An answer
// that stopped at max_tokens or was refused is returned as it is, with ok false and no error, for the caller to handle. Both calls use the
// request's RunInfo, so its budget covers them together.
func CallTool(ctx context.Context, c Client, req Request, tool string) (resp Response, call Block, ok bool, err error) {
	find := func(r Response) (Block, bool) {
		for _, u := range r.ToolUses() {
			if u.Name == tool {
				return u, true
			}
		}
		return Block{}, false
	}
	resp, err = c.Messages(ctx, req)
	if err != nil && (!errors.Is(err, ErrLedger) || len(resp.Content) == 0) {
		return resp, Block{}, false, err
	}
	if call, ok = find(resp); ok || resp.StopReason == "max_tokens" || resp.StopReason == "refusal" {
		return resp, call, ok, err
	}
	first := resp
	again := req
	again.Messages = append(append([]Message{}, req.Messages...), withContentOrText(first),
		Message{Role: RoleUser, Content: []Block{TextBlock(fmt.Sprintf(toolReminder, tool, tool))}})
	resp, err = c.Messages(ctx, again)
	// The usage of both calls is reported: the caller meters what it spent.
	resp.Usage = addUsage(first.Usage, resp.Usage)
	if err != nil && (!errors.Is(err, ErrLedger) || len(resp.Content) == 0) {
		return resp, Block{}, false, err
	}
	if call, ok = find(resp); ok || resp.StopReason == "max_tokens" || resp.StopReason == "refusal" {
		return resp, call, ok, err
	}
	return resp, Block{}, false, &NoToolCallError{Tool: tool, StopReason: resp.StopReason}
}

// withContentOrText is the assistant turn of an answer to send back (the API refuses an empty one).
func withContentOrText(r Response) Message {
	var content []Block
	for _, b := range r.Content {
		if b.Type == BlockText && b.Text == "" {
			continue
		}
		content = append(content, b)
	}
	if len(content) == 0 {
		content = []Block{TextBlock("(no answer)")}
	}
	return Message{Role: RoleAssistant, Content: content}
}

func addUsage(a, b Usage) Usage {
	return Usage{
		InputTokens: a.InputTokens + b.InputTokens, CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
		CacheWrite1hTokens: a.CacheWrite1hTokens + b.CacheWrite1hTokens, CacheReadTokens: a.CacheReadTokens + b.CacheReadTokens,
		OutputTokens: a.OutputTokens + b.OutputTokens, CacheWriteSplitMissing: a.CacheWriteSplitMissing || b.CacheWriteSplitMissing,
	}
}
