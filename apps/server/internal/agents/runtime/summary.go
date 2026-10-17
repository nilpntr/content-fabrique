package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// EventSummary is what the run feed shows of an event: its place, its kind and one short line of text. It never holds an image,
// the run context, the model's thinking or a whole tool result: the API returns summaries, and the dev CLI prints events itself.
type EventSummary struct {
	Seq  int32
	Type string
	// Text is one line (cut at SummaryChars), made of text a model or a tool wrote, so a client shows it as text, never as markup.
	Text string
	// Tool names the tool of a tool_call or tool_result.
	Tool    string
	IsError bool
}

// SummaryChars is the longest Text of an EventSummary, in bytes.
const SummaryChars = 400

// SummarizeEvent describes one event for the feed.
func SummarizeEvent(e store.AgentEvent) EventSummary {
	s := EventSummary{Seq: e.Seq, Type: e.Type}
	switch e.Type {
	case store.EventStatus:
		var p statusPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			break
		}
		s.Text = p.State
		for _, kv := range [][2]string{{"reason", p.Reason}, {"limit", p.Limit}, {"question", p.Question}} {
			if kv[1] != "" {
				s.Text += " " + kv[0] + "=" + kv[1]
			}
		}
	case store.EventAssistantMessage:
		var p assistantPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			break
		}
		var parts []string
		for _, b := range p.Content {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "tool_use":
				parts = append(parts, "[calls "+b.Name+"]")
			}
		}
		s.Text = strings.Join(parts, " ")
	case store.EventToolCall:
		var p toolCallPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			break
		}
		s.Tool = p.Name
		s.Text = p.Name + " " + string(p.Input)
	case store.EventToolResult:
		var p toolResultPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			break
		}
		s.Tool, s.IsError = p.Name, p.IsError
		images := 0
		var parts []string
		for _, b := range p.Content {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "image":
				images++
			}
		}
		if images > 0 {
			parts = append(parts, fmt.Sprintf("[%d images]", images))
		}
		s.Text = strings.Join(parts, " ")
	case store.EventError:
		var p errorPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			break
		}
		s.IsError = true
		s.Text = p.Code + ": " + p.Message
	case store.EventArtifact:
		var p struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			break
		}
		s.Text = p.Kind
	}
	s.Text = oneLine(s.Text, SummaryChars)
	return s
}

// QuestionOf returns the question of the status event that parked a run (waiting_input), or "".
func QuestionOf(e store.AgentEvent) string {
	var p statusPayload
	if e.Type != store.EventStatus || json.Unmarshal(e.Payload, &p) != nil || p.State != stateWaitingInput {
		return ""
	}
	return p.Question
}

// OutputSummary reads the output of a finished run: its message for the creator, and the timeline version the editor finished
// on (nil when the output names none).
func OutputSummary(output []byte) (summary string, timelineVersion *int) {
	if len(output) == 0 {
		return "", nil
	}
	var o struct {
		Summary         string `json:"summary"`
		TimelineVersion *int   `json:"timeline_version"`
	}
	if json.Unmarshal(output, &o) != nil {
		return "", nil
	}
	return o.Summary, o.TimelineVersion
}

// oneLine collapses white space and cuts s to at most n bytes at a rune boundary.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
