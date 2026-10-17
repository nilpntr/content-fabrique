// Package llm is the model access layer (P4-00a, docs/design/agent-runtime-v1.md section 2).
//
// Agents and tools see a Client: Messages and CountTokens. They never see an API key. ClientFactory.For hands out a
// Client for a workspace and a stage, together with the Billing label of its calls, and the factory is the one place
// that reads ANTHROPIC_API_KEY. Behind the Client sit, from the outside in:
//
//	metered   resolves image references, refuses a call that would break a spend cap, writes the usage_ledger row
//	cassette  LLM_MODE record or replay (a no-op in live)
//	limited   a per-key concurrency limit, and retries with backoff on 429 and 529 (retries live only here)
//	anthropic the SDK call, with the gateway headers when ANTHROPIC_BASE_URL points at a gateway
//
// Keep the Request and Response types free of SDK types: the agent runtime and its tests depend on this package,
// not on anthropic-sdk-go.
package llm

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// Client talks to the Messages API. A Client is bound to one workspace and one stage by the factory.
type Client interface {
	// Messages sends one request and returns the model's reply. When the error is non-nil the Response may still
	// carry usage (a call that consumed tokens and then failed); look at Response.Usage before discarding it.
	Messages(ctx context.Context, req Request) (Response, error)
	// CountTokens returns how many input tokens the request would use. It is free and generates nothing.
	CountTokens(ctx context.Context, req Request) (int64, error)
}

// Billing says who pays for the calls of a client; it is the ledger's billed_to.
type Billing string

// The three payers.
const (
	// BilledToPlatform is a managed workspace in staging or production: we pay.
	BilledToPlatform Billing = "platform"
	// BilledToWorkspaceKey is a BYOK workspace: the creator's own Anthropic key pays (P4-00b).
	BilledToWorkspaceKey Billing = "workspace_key"
	// BilledToTest is development, ci and bench traffic, kept apart so test spend never mixes into customer margin.
	BilledToTest Billing = "test"
)

// Role is the author of a message.
type Role string

// The two roles the Messages API has.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType is the kind of a content block.
type BlockType string

// The block kinds a request can carry and a response can return.
const (
	BlockText             BlockType = "text"
	BlockImage            BlockType = "image"
	BlockToolUse          BlockType = "tool_use"
	BlockToolResult       BlockType = "tool_result"
	BlockThinking         BlockType = "thinking"
	BlockRedactedThinking BlockType = "redacted_thinking"
)

// CacheControl marks a prompt-cache breakpoint at the end of the block that carries it.
type CacheControl struct {
	// TTL is "5m" (the default when empty) or "1h".
	TTL string
}

// Image is the picture of an image block: its bytes, or a reference to resolve before sending.
type Image struct {
	// MediaType is image/jpeg, image/png, image/gif or image/webp.
	MediaType string
	// Data holds the bytes. They are base64-encoded when sent, and the record/replay key uses their SHA-256.
	Data []byte
	// Ref names the image in storage (an R2 key, say) when Data is empty. Client.Messages resolves it through the
	// ImageResolver before sending, so the cassette key and the cost estimate see the real bytes.
	Ref string
}

// Block is one content block. Which fields matter depends on Type.
type Block struct {
	Type BlockType
	// Cache, on a request block, adds a cache breakpoint after it.
	Cache *CacheControl

	// Text (BlockText).
	Text string
	// Image (BlockImage).
	Image *Image
	// ID, Name and Input (BlockToolUse): the model's call. Input is the JSON object of arguments.
	ID    string
	Name  string
	Input json.RawMessage
	// ToolUseID, Content and IsError (BlockToolResult): the answer to a tool call. Content holds text and image blocks.
	ToolUseID string
	Content   []Block
	IsError   bool
	// Thinking and Signature (BlockThinking), Data (BlockRedactedThinking). Send them back unchanged when the model
	// used a tool, so the thinking round trip stays valid.
	Thinking  string
	Signature string
	Data      string
}

// TextBlock is a text block, the common case.
func TextBlock(text string) Block { return Block{Type: BlockText, Text: text} }

// Message is one turn of the conversation.
type Message struct {
	Role    Role
	Content []Block
}

// Tool describes a function the model may call.
type Tool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the arguments: an object schema.
	InputSchema json.RawMessage
	// Strict makes the API guarantee that the arguments match the schema. A strict schema must set
	// additionalProperties to false on every object.
	Strict bool
	// Cache adds a cache breakpoint after this tool definition.
	Cache *CacheControl
}

// ToolChoice constrains whether and which tool the model calls. The zero value is the API's default (auto).
type ToolChoice struct {
	// Type is "auto", "any", "tool" or "none".
	Type string
	// Name is the tool, for Type "tool".
	Name string
}

// ThinkingMode picks how the model thinks.
type ThinkingMode string

// The thinking modes.
const (
	// ThinkingAdaptive lets the model decide when and how much to think; effort steers it.
	ThinkingAdaptive ThinkingMode = "adaptive"
	// ThinkingEnabled is manual extended thinking with a token budget.
	ThinkingEnabled ThinkingMode = "enabled"
	// ThinkingDisabled turns thinking off.
	ThinkingDisabled ThinkingMode = "disabled"
)

// Thinking configures the model's thinking. A nil *Thinking in a Request leaves the model's default.
type Thinking struct {
	Mode ThinkingMode
	// BudgetTokens is required (at least 1024) for ThinkingEnabled and counts toward max_tokens.
	BudgetTokens int64
	// Display is "summarized" or "omitted" (omit the thinking text from the response); empty is the API's default.
	Display string
}

// Budget is the dollar budget of one agent run. The runtime implements it over agent_runs.budget_micros.
type Budget interface {
	// Remaining is how many micro-dollars the run may still spend.
	Remaining() int64
	// Spend records that a call cost micros (called after the call, with its list cost).
	Spend(micros int64)
}

// RunInfo says which agent run a request belongs to. The zero value is a call outside a run.
type RunInfo struct {
	// ID is the run's id (uuid.Nil outside a run). It goes to the ledger and the gateway's cf-aig-metadata.
	ID uuid.UUID
	// Profile is the routing profile of the run, for the gateway metadata and the ledger log.
	Profile string
	// Budget is the run's dollar budget: a call is refused before it is sent when its worst-case cost exceeds
	// Remaining. Nil means no per-run limit.
	Budget Budget
}

// Request is one Messages API call.
type Request struct {
	Model string
	// System holds the system prompt as text blocks (each may carry a cache breakpoint).
	System   []Block
	Messages []Message
	Tools    []Tool
	// ToolChoice is optional.
	ToolChoice *ToolChoice
	// MaxTokens caps the output, thinking included. Required.
	MaxTokens int
	// Effort is low, medium, high, xhigh or max (output_config.effort); empty leaves the model's default.
	Effort string
	// Thinking is optional.
	Thinking *Thinking
	// StopSequences are optional custom stop strings.
	StopSequences []string

	// Run says which run this call belongs to. It is not sent to the API (except as gateway metadata) and is not
	// part of the record/replay key.
	Run RunInfo
}

// Usage is what a call consumed, as the API reports it.
type Usage struct {
	// InputTokens excludes cache reads and cache writes.
	InputTokens int64 `json:"input_tokens"`
	// CacheWriteTokens is the total written to the cache, of either lifetime.
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// CacheWrite1hTokens is the part of CacheWriteTokens written with the one-hour lifetime.
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
}

// IsZero reports whether nothing was consumed.
func (u Usage) IsZero() bool { return u == Usage{} }

// PromptTokens is the length of the prompt: input plus cache writes plus cache reads.
func (u Usage) PromptTokens() int64 { return u.InputTokens + u.CacheWriteTokens + u.CacheReadTokens }

// Response is the model's reply.
type Response struct {
	// ID is the message id; RequestID is Anthropic's request-id header, the one to quote in a support ticket.
	ID        string
	RequestID string
	Model     string
	Content   []Block
	// StopReason is end_turn, max_tokens, stop_sequence, tool_use, pause_turn or refusal.
	StopReason string
	Usage      Usage
}

// Text returns the concatenated text blocks of the reply.
func (r Response) Text() string {
	var s string
	for _, b := range r.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}

// ToolUses returns the tool_use blocks of the reply, in order.
func (r Response) ToolUses() []Block {
	var out []Block
	for _, b := range r.Content {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// ImageResolver turns an Image.Ref into bytes. The runtime supplies one over R2 (assets and keyframes).
type ImageResolver interface {
	ResolveImage(ctx context.Context, ref string) (mediaType string, data []byte, err error)
}
