package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/google/uuid"
)

// anthropicHost is the direct API. A base URL on another host is a gateway.
const anthropicHost = "api.anthropic.com"

// defaultRequestTimeout is the timeout of one non-streaming request. It also tells the SDK not to refuse a large
// max_tokens for "may take longer than 10 minutes" (streaming arrives with the runtime if it needs it).
const defaultRequestTimeout = 10 * time.Minute

// isGateway reports whether baseURL points at a gateway (anything but empty and api.anthropic.com).
func isGateway(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return true
	}
	return !strings.EqualFold(u.Hostname(), anthropicHost)
}

// anthropicConfig builds one anthropicClient. The factory is the only caller.
type anthropicConfig struct {
	// APIKey is the Anthropic key. It lives only inside the SDK client and the scrub list.
	APIKey string
	// BaseURL is ANTHROPIC_BASE_URL; empty is api.anthropic.com.
	BaseURL string
	// GatewayToken is CF_AIG_TOKEN, sent as cf-aig-authorization to an authenticated gateway.
	GatewayToken string
	// HTTPClient replaces the default transport (tests use a fake one).
	HTTPClient *http.Client
	// Workspace and Stage label the gateway's cf-aig-metadata.
	Workspace string
	Stage     string
	Timeout   time.Duration
}

// anthropicClient is the Client over anthropic-sdk-go. It does not retry (MaxRetries 0): retries live in limited.
type anthropicClient struct {
	sdk       anthropic.Client
	gateway   bool
	workspace string
	stage     string
	// secrets are removed from every error message that leaves this type.
	secrets []string
}

func newAnthropicClient(cfg anthropicConfig) *anthropicClient {
	c := &anthropicClient{gateway: isGateway(cfg.BaseURL), workspace: cfg.Workspace, stage: cfg.Stage}
	for _, s := range []string{cfg.APIKey, cfg.GatewayToken} {
		if s != "" {
			c.secrets = append(c.secrets, s)
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	// Explicit options only: the SDK's own environment defaults (ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL ...) must not
	// decide which key or endpoint is used.
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(0),
		option.WithRequestTimeout(timeout),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	if c.gateway {
		// The gateway keeps metadata (tokens, model, cost, duration) and not prompts or frames.
		opts = append(opts, option.WithHeader("cf-aig-collect-log-payload", "false"))
		if cfg.GatewayToken != "" {
			opts = append(opts, option.WithHeader("cf-aig-authorization", "Bearer "+cfg.GatewayToken))
		}
	}
	c.sdk = anthropic.NewClient(opts...)
	return c
}

// gatewayMetadata is the cf-aig-metadata header value: at most five entries (the gateway's limit), four used.
func (c *anthropicClient) gatewayMetadata(run RunInfo) string {
	m := map[string]string{"workspace_id": c.workspace, "stage": c.stage}
	if run.ID != uuid.Nil {
		m["run_id"] = run.ID.String()
	}
	if run.Profile != "" {
		m["profile"] = run.Profile
	}
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	b, _ := json.Marshal(m) // a map of strings cannot fail
	return string(b)
}

func (c *anthropicClient) requestOptions(run RunInfo) []option.RequestOption {
	if !c.gateway {
		return nil
	}
	return []option.RequestOption{option.WithHeader("cf-aig-metadata", c.gatewayMetadata(run))}
}

// Messages implements Client.
func (c *anthropicClient) Messages(ctx context.Context, req Request) (Response, error) {
	params, err := toMessageParams(req)
	if err != nil {
		return Response{}, err
	}
	var raw *http.Response
	opts := append(c.requestOptions(req.Run), option.WithResponseInto(&raw))
	msg, err := c.sdk.Messages.New(ctx, params, opts...)
	if err != nil {
		return Response{}, c.convertError(err)
	}
	resp := fromMessage(msg)
	if raw != nil {
		resp.RequestID = raw.Header.Get("request-id")
	}
	return resp, nil
}

// CountTokens implements Client.
func (c *anthropicClient) CountTokens(ctx context.Context, req Request) (int64, error) {
	params, err := toCountParams(req)
	if err != nil {
		return 0, err
	}
	res, err := c.sdk.Messages.CountTokens(ctx, params, c.requestOptions(req.Run)...)
	if err != nil {
		return 0, c.convertError(err)
	}
	return res.InputTokens, nil
}

// convertError turns an SDK error into *APIError (for an answer from the server) or a scrubbed wrapper.
func (c *anthropicClient) convertError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		e := &APIError{Status: apiErr.StatusCode, RequestID: apiErr.RequestID, Type: string(apiErr.Type())}
		if apiErr.Response != nil {
			e.RetryAfter = parseRetryAfter(apiErr.Response.Header.Get("retry-after"), time.Now())
			if e.RequestID == "" {
				e.RequestID = apiErr.Response.Header.Get("request-id")
			}
		}
		var env struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(apiErr.RawJSON()), &env) == nil {
			if e.Type == "" {
				e.Type = env.Error.Type
			}
			e.Message = scrub(env.Error.Message, c.secrets)
		}
		return e
	}
	return &scrubbedError{msg: "llm: request failed: " + scrub(err.Error(), c.secrets), err: err}
}

// scrubbedError carries an error whose text had secrets removed. Unwrap keeps errors.Is(err, context.Canceled) and
// friends working; the unwrapped value is not printed by this type.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrub removes every secret from s.
func scrub(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

// parseRetryAfter reads the retry-after header: whole or fractional seconds, or an HTTP date. Zero when absent or
// unreadable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 || secs > 24*3600 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 && d < 24*time.Hour {
			return d
		}
	}
	return 0
}

// ---- Request -> SDK params -----------------------------------------------------------------------------------

func cacheParam(c *CacheControl) (anthropic.CacheControlEphemeralParam, error) {
	if c == nil {
		return anthropic.CacheControlEphemeralParam{}, nil
	}
	switch c.TTL {
	case "", "5m":
		return anthropic.CacheControlEphemeralParam{}, nil
	case "1h":
		return anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL1h}, nil
	}
	return anthropic.CacheControlEphemeralParam{}, fmt.Errorf("llm: cache_control ttl %q is not 5m or 1h", c.TTL)
}

// markCache is the param for a block that has a breakpoint: with Cache set it must marshal "cache_control", which the
// zero CacheControlEphemeralParam omits, so a 5m mark is spelled out.
func markCache(c *CacheControl) (anthropic.CacheControlEphemeralParam, error) {
	p, err := cacheParam(c)
	if err != nil {
		return p, err
	}
	if c != nil && p.TTL == "" {
		p.TTL = anthropic.CacheControlEphemeralTTLTTL5m
	}
	return p, nil
}

func imageBlock(img *Image, cache *CacheControl) (anthropic.ContentBlockParamUnion, error) {
	if img == nil {
		return anthropic.ContentBlockParamUnion{}, errors.New("llm: image block has no image")
	}
	if len(img.Data) == 0 {
		return anthropic.ContentBlockParamUnion{}, errors.New("llm: image block has no bytes (a reference must be resolved before sending)")
	}
	cc, err := markCache(cache)
	if err != nil {
		return anthropic.ContentBlockParamUnion{}, err
	}
	b := anthropic.NewImageBlockBase64(img.MediaType, base64.StdEncoding.EncodeToString(img.Data))
	b.OfImage.CacheControl = cc
	return b, nil
}

func textParam(text string, cache *CacheControl) (anthropic.TextBlockParam, error) {
	cc, err := markCache(cache)
	return anthropic.TextBlockParam{Text: text, CacheControl: cc}, err
}

func toBlock(b Block) (anthropic.ContentBlockParamUnion, error) {
	switch b.Type {
	case BlockText:
		t, err := textParam(b.Text, b.Cache)
		return anthropic.ContentBlockParamUnion{OfText: &t}, err
	case BlockImage:
		return imageBlock(b.Image, b.Cache)
	case BlockToolUse:
		cc, err := markCache(b.Cache)
		if err != nil {
			return anthropic.ContentBlockParamUnion{}, err
		}
		input := b.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		return anthropic.ContentBlockParamUnion{OfToolUse: &anthropic.ToolUseBlockParam{ID: b.ID, Name: b.Name, Input: input, CacheControl: cc}}, nil
	case BlockToolResult:
		cc, err := markCache(b.Cache)
		if err != nil {
			return anthropic.ContentBlockParamUnion{}, err
		}
		tr := &anthropic.ToolResultBlockParam{ToolUseID: b.ToolUseID, CacheControl: cc}
		if b.IsError {
			tr.IsError = anthropic.Bool(true)
		}
		for _, inner := range b.Content {
			switch inner.Type {
			case BlockText:
				t, err := textParam(inner.Text, inner.Cache)
				if err != nil {
					return anthropic.ContentBlockParamUnion{}, err
				}
				tr.Content = append(tr.Content, anthropic.ToolResultBlockParamContentUnion{OfText: &t})
			case BlockImage:
				ib, err := imageBlock(inner.Image, inner.Cache)
				if err != nil {
					return anthropic.ContentBlockParamUnion{}, err
				}
				tr.Content = append(tr.Content, anthropic.ToolResultBlockParamContentUnion{OfImage: ib.OfImage})
			default:
				return anthropic.ContentBlockParamUnion{}, fmt.Errorf("llm: a tool_result holds text and image blocks, not %q", inner.Type)
			}
		}
		return anthropic.ContentBlockParamUnion{OfToolResult: tr}, nil
	case BlockThinking:
		return anthropic.NewThinkingBlock(b.Signature, b.Thinking), nil
	case BlockRedactedThinking:
		return anthropic.NewRedactedThinkingBlock(b.Data), nil
	}
	return anthropic.ContentBlockParamUnion{}, fmt.Errorf("llm: unknown block type %q", b.Type)
}

func toMessages(msgs []Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for i, m := range msgs {
		var role anthropic.MessageParamRole
		switch m.Role {
		case RoleUser:
			role = anthropic.MessageParamRoleUser
		case RoleAssistant:
			role = anthropic.MessageParamRoleAssistant
		default:
			return nil, fmt.Errorf("llm: message %d has role %q", i, m.Role)
		}
		mp := anthropic.MessageParam{Role: role}
		for _, b := range m.Content {
			pb, err := toBlock(b)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			mp.Content = append(mp.Content, pb)
		}
		out = append(out, mp)
	}
	return out, nil
}

func toSystem(blocks []Block) ([]anthropic.TextBlockParam, error) {
	var out []anthropic.TextBlockParam
	for _, b := range blocks {
		if b.Type != BlockText {
			return nil, errors.New("llm: system blocks must be text")
		}
		t, err := textParam(b.Text, b.Cache)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// toTool converts a Tool. The schema keeps every key it has (additionalProperties, $defs ...): the SDK's typed
// fields take properties and required, the rest travels as extra fields.
func toTool(t Tool) (anthropic.ToolParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil || schema == nil {
		return anthropic.ToolParam{}, fmt.Errorf("llm: tool %q: input_schema must be a JSON object", t.Name)
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		return anthropic.ToolParam{}, fmt.Errorf("llm: tool %q: input_schema must be an object schema", t.Name)
	}
	if t.Strict {
		if ap, ok := schema["additionalProperties"].(bool); !ok || ap {
			return anthropic.ToolParam{}, fmt.Errorf("llm: tool %q is strict, so its schema must set additionalProperties to false", t.Name)
		}
	}
	is := anthropic.ToolInputSchemaParam{ExtraFields: map[string]any{}}
	for k, v := range schema {
		switch k {
		case "type":
		case "properties":
			is.Properties = v
		case "required":
			arr, ok := v.([]any)
			if !ok {
				return anthropic.ToolParam{}, fmt.Errorf("llm: tool %q: required must be a list", t.Name)
			}
			for _, r := range arr {
				s, ok := r.(string)
				if !ok {
					return anthropic.ToolParam{}, fmt.Errorf("llm: tool %q: required must list names", t.Name)
				}
				is.Required = append(is.Required, s)
			}
		default:
			is.ExtraFields[k] = v
		}
	}
	cc, err := markCache(t.Cache)
	if err != nil {
		return anthropic.ToolParam{}, err
	}
	tp := anthropic.ToolParam{Name: t.Name, InputSchema: is, CacheControl: cc}
	if t.Description != "" {
		tp.Description = anthropic.String(t.Description)
	}
	if t.Strict {
		tp.Strict = anthropic.Bool(true)
	}
	return tp, nil
}

func toThinking(t *Thinking) (anthropic.ThinkingConfigParamUnion, error) {
	if t == nil {
		return anthropic.ThinkingConfigParamUnion{}, nil
	}
	switch t.Mode {
	case ThinkingAdaptive:
		a := &anthropic.ThinkingConfigAdaptiveParam{}
		switch t.Display {
		case "":
		case "summarized":
			a.Display = anthropic.ThinkingConfigAdaptiveDisplaySummarized
		case "omitted":
			a.Display = anthropic.ThinkingConfigAdaptiveDisplayOmitted
		default:
			return anthropic.ThinkingConfigParamUnion{}, fmt.Errorf("llm: thinking display %q is not summarized or omitted", t.Display)
		}
		return anthropic.ThinkingConfigParamUnion{OfAdaptive: a}, nil
	case ThinkingEnabled:
		if t.BudgetTokens < 1024 {
			return anthropic.ThinkingConfigParamUnion{}, errors.New("llm: enabled thinking needs a budget of at least 1024 tokens")
		}
		e := &anthropic.ThinkingConfigEnabledParam{BudgetTokens: t.BudgetTokens}
		switch t.Display {
		case "":
		case "summarized":
			e.Display = anthropic.ThinkingConfigEnabledDisplaySummarized
		case "omitted":
			e.Display = anthropic.ThinkingConfigEnabledDisplayOmitted
		default:
			return anthropic.ThinkingConfigParamUnion{}, fmt.Errorf("llm: thinking display %q is not summarized or omitted", t.Display)
		}
		return anthropic.ThinkingConfigParamUnion{OfEnabled: e}, nil
	case ThinkingDisabled:
		d := anthropic.NewThinkingConfigDisabledParam()
		return anthropic.ThinkingConfigParamUnion{OfDisabled: &d}, nil
	}
	return anthropic.ThinkingConfigParamUnion{}, fmt.Errorf("llm: thinking mode %q is not adaptive, enabled or disabled", t.Mode)
}

func toToolChoice(tc *ToolChoice) (anthropic.ToolChoiceUnionParam, error) {
	if tc == nil || tc.Type == "" {
		return anthropic.ToolChoiceUnionParam{}, nil
	}
	switch tc.Type {
	case "auto":
		return anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}, nil
	case "any":
		return anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}, nil
	case "none":
		return anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}, nil
	case "tool":
		if tc.Name == "" {
			return anthropic.ToolChoiceUnionParam{}, errors.New("llm: tool_choice tool needs a name")
		}
		return anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: tc.Name}}, nil
	}
	return anthropic.ToolChoiceUnionParam{}, fmt.Errorf("llm: tool_choice %q is not auto, any, tool or none", tc.Type)
}

func toOutputConfig(effort string) anthropic.OutputConfigParam {
	return anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(effort)}
}

func validateRequest(req Request, forCount bool) error {
	if req.Model == "" {
		return errors.New("llm: request has no model")
	}
	if !forCount && req.MaxTokens < 1 {
		return errors.New("llm: request has no max_tokens")
	}
	if len(req.Messages) == 0 {
		return errors.New("llm: request has no messages")
	}
	return nil
}

func toMessageParams(req Request) (anthropic.MessageNewParams, error) {
	if err := validateRequest(req, false); err != nil {
		return anthropic.MessageNewParams{}, err
	}
	msgs, err := toMessages(req.Messages)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	p := anthropic.MessageNewParams{Model: anthropic.Model(req.Model), MaxTokens: int64(req.MaxTokens), Messages: msgs, StopSequences: req.StopSequences}
	if p.System, err = toSystem(req.System); err != nil {
		return p, err
	}
	for _, t := range req.Tools {
		tp, err := toTool(t)
		if err != nil {
			return p, err
		}
		p.Tools = append(p.Tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	if p.ToolChoice, err = toToolChoice(req.ToolChoice); err != nil {
		return p, err
	}
	if p.Thinking, err = toThinking(req.Thinking); err != nil {
		return p, err
	}
	if req.Effort != "" {
		p.OutputConfig = toOutputConfig(req.Effort)
	}
	return p, nil
}

func toCountParams(req Request) (anthropic.MessageCountTokensParams, error) {
	if err := validateRequest(req, true); err != nil {
		return anthropic.MessageCountTokensParams{}, err
	}
	msgs, err := toMessages(req.Messages)
	if err != nil {
		return anthropic.MessageCountTokensParams{}, err
	}
	p := anthropic.MessageCountTokensParams{Model: anthropic.Model(req.Model), Messages: msgs}
	sys, err := toSystem(req.System)
	if err != nil {
		return p, err
	}
	if len(sys) > 0 {
		p.System = anthropic.MessageCountTokensParamsSystemUnion{OfTextBlockArray: sys}
	}
	for _, t := range req.Tools {
		tp, err := toTool(t)
		if err != nil {
			return p, err
		}
		p.Tools = append(p.Tools, anthropic.MessageCountTokensToolUnionParam{OfTool: &tp})
	}
	if p.ToolChoice, err = toToolChoice(req.ToolChoice); err != nil {
		return p, err
	}
	if p.Thinking, err = toThinking(req.Thinking); err != nil {
		return p, err
	}
	if req.Effort != "" {
		p.OutputConfig = toOutputConfig(req.Effort)
	}
	return p, nil
}

// ---- SDK message -> Response ---------------------------------------------------------------------------------

func fromMessage(m *anthropic.Message) Response {
	r := Response{ID: m.ID, Model: string(m.Model), StopReason: string(m.StopReason), Usage: fromUsage(m.Usage)}
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			r.Content = append(r.Content, Block{Type: BlockText, Text: b.Text})
		case "thinking":
			r.Content = append(r.Content, Block{Type: BlockThinking, Thinking: b.Thinking, Signature: b.Signature})
		case "redacted_thinking":
			r.Content = append(r.Content, Block{Type: BlockRedactedThinking, Data: b.Data})
		case "tool_use":
			in := b.Input
			if len(in) == 0 {
				in = json.RawMessage("{}")
			}
			r.Content = append(r.Content, Block{Type: BlockToolUse, ID: b.ID, Name: b.Name, Input: append(json.RawMessage(nil), in...)})
		default:
			// Server tool blocks (web_search ...) are not used yet. Keep a marker so a caller sees the reply was not empty.
			r.Content = append(r.Content, Block{Type: BlockType(b.Type)})
		}
	}
	return r
}

func fromUsage(u anthropic.Usage) Usage {
	out := Usage{
		InputTokens:      u.InputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		OutputTokens:     u.OutputTokens,
	}
	// cache_creation splits the writes by lifetime; the total is cache_creation_input_tokens. If a gateway drops the
	// split, every write is priced as the cheaper 5-minute kind.
	out.CacheWrite1hTokens = min(u.CacheCreation.Ephemeral1hInputTokens, out.CacheWriteTokens)
	return out
}
