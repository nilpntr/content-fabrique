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
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
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

	// Raw is the block's JSON, kept for a block type this package does not model (server_tool_use,
	// web_search_tool_result, ...). Such a block has Type set to what the API said and nothing else: append the
	// assistant turn as it came and Raw is sent back verbatim, which the API needs for server tools and pause_turn.
	Raw json.RawMessage
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

// Budget is the dollar budget of one agent run. The runtime implements it over agent_runs.budget_micros (RunBudget is
// the in-memory implementation to build on). It works by reservation, so concurrent calls of one run can never
// overspend: each call sets its worst case aside before it is sent and settles with the actual cost afterwards.
type Budget interface {
	// Reserve sets micros aside, or returns an error (a *CapError of kind run) when the run does not have that much
	// left after the reservations already held. On success the caller must call release exactly once, with the call's
	// actual list cost: release(0) when nothing was spent, and the reserved amount itself when the outcome is unknown
	// (the request was sent and no usage came back).
	Reserve(micros int64) (release func(actual int64), err error)
}

// RunBudget is a Budget over a fixed limit, safe for concurrent use.
type RunBudget struct {
	mu       sync.Mutex
	limit    int64
	reserved int64
	spent    int64
}

// NewRunBudget returns a budget of limitMicros micro-dollars.
func NewRunBudget(limitMicros int64) *RunBudget { return &RunBudget{limit: limitMicros} }

// Reserve implements Budget.
func (b *RunBudget) Reserve(micros int64) (func(actual int64), error) {
	if micros < 0 {
		return nil, errors.New("llm: a reservation cannot be negative")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := max(b.limit-b.spent-b.reserved, 0); micros > room {
		return nil, &CapError{Kind: CapRun, EstimateMicros: micros, LimitMicros: room}
	}
	b.reserved += micros
	var once sync.Once
	return func(actual int64) {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.reserved -= micros
			b.spent += max(actual, 0)
		})
	}, nil
}

// Spent is what the run has used, settled calls only.
func (b *RunBudget) Spent() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.spent }

// Remaining is what is left after settled spend and the reservations in flight.
func (b *RunBudget) Remaining() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.spent - b.reserved
}

// RunInfo says which agent run a request belongs to. The zero value is a call outside a run.
type RunInfo struct {
	// ID is the run's id (uuid.Nil outside a run). It goes to the ledger and the gateway's cf-aig-metadata.
	ID uuid.UUID
	// Profile is the routing profile of the run, for the gateway metadata and the ledger log.
	Profile string
	// ProjectID and MemberUserID attribute the call in the ledger: the project the run edits and the member who started it (both
	// optional; the zero value is none). The ledger's action_id is the run's ID until P7-01a introduces real actions.
	ProjectID    ids.ProjectID
	MemberUserID ids.UserID
	// WaitOnBusy makes a call that the platform breaker's room could not take right now (spend_cap_busy: a large request next to the
	// live holds near the cap, not a trip) wait and try again with backoff, until the context ends. An agent run sets it: its context
	// ends at the run's wall clock, which bounds the wait. Without it the refusal is returned at once.
	WaitOnBusy bool
	// Budget is the run's dollar budget: a call reserves its worst-case cost before it is sent and is refused when
	// the run cannot cover it. Nil means no per-run limit.
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
	// CacheWriteSplitMissing says the response had cache writes but not their split by lifetime (a gateway dropped
	// cache_creation). Pricing then assumes the dearer 1-hour rate when the request asked for a 1-hour breakpoint.
	CacheWriteSplitMissing bool `json:"cache_write_split_missing,omitempty"`
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

// ImageResolver turns an Image.Ref into bytes. The runtime supplies one over R2 (assets and keyframes). The context of
// ResolveImage carries the workspace of the client that sends the request (WorkspaceFromContext), so a resolver can refuse
// a reference that is not that workspace's.
type ImageResolver interface {
	ResolveImage(ctx context.Context, ref string) (mediaType string, data []byte, err error)
}

type workspaceKey struct{}

// WithWorkspace returns a context that carries the workspace a client is bound to.
func WithWorkspace(ctx context.Context, ws ids.WorkspaceID) context.Context {
	return context.WithValue(ctx, workspaceKey{}, ws)
}

// WorkspaceFromContext returns the workspace of the client whose request is being sent, and false outside one.
func WorkspaceFromContext(ctx context.Context) (ids.WorkspaceID, bool) {
	ws, ok := ctx.Value(workspaceKey{}).(ids.WorkspaceID)
	return ws, ok && !ws.IsZero()
}
