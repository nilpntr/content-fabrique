package telemetry

import (
	"bytes"
	"encoding/json"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
)

// OpenInference semantic conventions (https://github.com/Arize-ai/openinference, the spec Phoenix reads): the span kinds and the
// attribute names the agent spans use. The names are strings in the spec, kept here in one place.
const (
	SpanKind = attribute.Key("openinference.span.kind")

	KindAgent     = "AGENT"
	KindLLM       = "LLM"
	KindTool      = "TOOL"
	KindChain     = "CHAIN"
	KindEvaluator = "EVALUATOR"

	InputValue     = attribute.Key("input.value")
	InputMimeType  = attribute.Key("input.mime_type")
	OutputValue    = attribute.Key("output.value")
	OutputMimeType = attribute.Key("output.mime_type")
	SessionID      = attribute.Key("session.id")
	Metadata       = attribute.Key("metadata")

	LLMModelName            = attribute.Key("llm.model_name")
	LLMProvider             = attribute.Key("llm.provider")
	LLMSystem               = attribute.Key("llm.system")
	LLMInvocationParameters = attribute.Key("llm.invocation_parameters")
	LLMTokensPrompt         = attribute.Key("llm.token_count.prompt")
	LLMTokensCompletion     = attribute.Key("llm.token_count.completion")
	LLMTokensTotal          = attribute.Key("llm.token_count.total")
	LLMTokensCacheRead      = attribute.Key("llm.token_count.prompt_details.cache_read")
	LLMTokensCacheWrite     = attribute.Key("llm.token_count.prompt_details.cache_write")
	LLMCostTotal            = attribute.Key("llm.cost.total")

	ToolName        = attribute.Key("tool.name")
	ToolDescription = attribute.Key("tool.description")
	ToolParameters  = attribute.Key("tool.parameters")

	MimeText = "text/plain"
	MimeJSON = "application/json"
)

// Kind is the openinference.span.kind attribute.
func Kind(kind string) attribute.KeyValue { return SpanKind.String(kind) }

// Text is input.value or output.value as plain text, redacted and capped.
func Text(key attribute.Key, s string) []attribute.KeyValue {
	mime := InputMimeType
	if key == OutputValue {
		mime = OutputMimeType
	}
	return []attribute.KeyValue{key.String(Clean(s, MaxValueBytes)), mime.String(MimeText)}
}

// JSONValue is input.value or output.value as JSON. A value whose encoding is over MaxValueBytes is sent as capped text instead (a
// cut JSON document would not parse).
func JSONValue(key attribute.Key, v any) []attribute.KeyValue {
	mime := InputMimeType
	if key == OutputValue {
		mime = OutputMimeType
	}
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = marshal(v); err != nil {
			return nil
		}
	}
	s := Redact(string(raw))
	if len(s) > MaxValueBytes {
		return []attribute.KeyValue{key.String(Cap(s, MaxValueBytes)), mime.String(MimeText)}
	}
	return []attribute.KeyValue{key.String(s), mime.String(MimeJSON)}
}

// JSONString is any value as a compact JSON string attribute (llm.invocation_parameters, tool.parameters, metadata), redacted and capped.
func JSONString(key attribute.Key, v any) attribute.KeyValue {
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = marshal(v); err != nil {
			return key.String("")
		}
	}
	return key.String(Clean(string(raw), MaxValueBytes))
}

func marshal(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Message is one message of an LLM span, in OpenInference's flattened form.
type Message struct {
	Role string
	// Parts are the message's contents in order.
	Parts []Part
	// ToolCalls are the tool calls of an assistant message.
	ToolCalls []ToolCall
	// ToolCallID is set on a tool result message (role "tool").
	ToolCallID string
}

// Part is one content part: text, or an image by URL (a data: URL of a thumbnail, or a reference such as r2://key).
type Part struct {
	Text     string
	ImageURL string
}

// ToolCall is a tool call of an assistant message.
type ToolCall struct {
	ID, Name  string
	Arguments string
}

// MaxSpanContentBytes bounds the messages of one LLM span. Every call of a long agent run sends the whole conversation again, so a span
// keeps the first two messages (the system prompt and the run context) and as many of the newest as fit; the ones in between are
// replaced by a note of how many were left out (each one is in the span of the call that first sent it).
const MaxSpanContentBytes = 384 << 10

func messageSize(m Message) int {
	n := len(m.Role) + len(m.ToolCallID)
	for _, p := range m.Parts {
		n += min(len(p.Text), MaxTextBytes) + len(p.ImageURL) + 64
	}
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Name) + min(len(tc.Arguments), MaxTextBytes) + 64
	}
	return n
}

// fitMessages applies MaxSpanContentBytes.
func fitMessages(msgs []Message) []Message {
	total := 0
	for _, m := range msgs {
		total += messageSize(m)
	}
	if total <= MaxSpanContentBytes || len(msgs) <= 3 {
		return msgs
	}
	head := msgs[:2]
	budget := MaxSpanContentBytes - messageSize(head[0]) - messageSize(head[1])
	start := len(msgs)
	for start > 2 && budget-messageSize(msgs[start-1]) >= 0 {
		budget -= messageSize(msgs[start-1])
		start--
	}
	if start == len(msgs) { // keep the newest one whatever its size: it is what this call added
		start--
	}
	out := append([]Message{}, head...)
	if omitted := start - 2; omitted > 0 {
		out = append(out, Message{Role: "user", Parts: []Part{{Text: "[" + strconv.Itoa(omitted) + " earlier messages left out of this span; the spans of the earlier calls have them]"}}})
	}
	return append(out, msgs[start:]...)
}

// Messages flattens messages under prefix ("llm.input_messages" or "llm.output_messages"). Text is redacted and capped per part, and the
// whole is held to MaxSpanContentBytes.
func Messages(prefix string, msgs []Message) []attribute.KeyValue {
	var out []attribute.KeyValue
	for i, m := range fitMessages(msgs) {
		p := prefix + "." + strconv.Itoa(i) + ".message."
		out = append(out, attribute.String(p+"role", m.Role))
		if m.ToolCallID != "" {
			out = append(out, attribute.String(p+"tool_call_id", m.ToolCallID))
		}
		for j, part := range m.Parts {
			c := p + "contents." + strconv.Itoa(j) + ".message_content."
			if part.ImageURL != "" {
				out = append(out, attribute.String(c+"type", "image"), attribute.String(c+"image.image.url", part.ImageURL))
				continue
			}
			out = append(out, attribute.String(c+"type", "text"), attribute.String(c+"text", Clean(part.Text, MaxTextBytes)))
		}
		for k, tc := range m.ToolCalls {
			c := p + "tool_calls." + strconv.Itoa(k) + ".tool_call."
			out = append(out, attribute.String(c+"id", tc.ID), attribute.String(c+"function.name", tc.Name),
				attribute.String(c+"function.arguments", Clean(tc.Arguments, MaxTextBytes)))
		}
	}
	return out
}
