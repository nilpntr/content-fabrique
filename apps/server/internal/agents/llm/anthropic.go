package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/google/uuid"
)

// anthropicHost is the direct API. A base URL on another host is a gateway.
const anthropicHost = "api.anthropic.com"

// Timeouts of one non-streaming request. Setting an explicit timeout also tells the SDK not to refuse a large
// max_tokens for "may take longer than 10 minutes". The timeout follows max_tokens and the model's speed instead of
// being a flat number, because a call cut off after the model finished is paid for and lost.
const (
	countTokensTimeout = 2 * time.Minute
	minRequestTimeout  = 10 * time.Minute
	maxRequestTimeout  = 60 * time.Minute
	requestTimeoutBase = 2 * time.Minute
	// timeoutSafety multiplies the generation time the model's throughput predicts.
	timeoutSafety = 3
)

// outputTokensPerSecond is a deliberately slow guess at a model family's generation speed, for the timeout only.
func outputTokensPerSecond(model string) float64 {
	switch {
	case strings.Contains(model, "haiku"):
		return 80
	case strings.Contains(model, "sonnet"):
		return 40
	default: // opus and anything unknown
		return 20
	}
}

// requestTimeout is how long one Messages call may take: two minutes plus three times the time max_tokens takes at the
// model's slow-case speed, between 10 and 60 minutes. Opus 5.5 with max_tokens 16000 gets 42 minutes.
func requestTimeout(model string, maxTokens int) time.Duration {
	gen := time.Duration(float64(maxTokens) / outputTokensPerSecond(model) * float64(time.Second))
	return min(max(requestTimeoutBase+timeoutSafety*gen, minRequestTimeout), maxRequestTimeout)
}

// callState records, for one call, whether its request reached the wire. It travels in the request context and is
// filled by trackingTransport.
type callState struct{ sent atomic.Bool }

type callStateKey struct{}

// trackingTransport notes when a request has been written to the connection (net/http's own trace), or answered. A
// failure before that is safe to retry; one after it may have been billed.
type trackingTransport struct{ base http.RoundTripper }

func (t trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	st, _ := r.Context().Value(callStateKey{}).(*callState)
	if st != nil {
		r = r.WithContext(httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{
			WroteRequest: func(httptrace.WroteRequestInfo) { st.sent.Store(true) },
		}))
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil && st != nil {
		st.sent.Store(true)
	}
	return resp, err
}

// trackedClient wraps the HTTP client (the default one when nil) so its transport reports whether a request was sent.
func trackedClient(hc *http.Client) *http.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Transport: trackingTransport{base: base}, CheckRedirect: hc.CheckRedirect, Jar: hc.Jar, Timeout: hc.Timeout}
}

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
	// Timeout overrides the per-call timeout (tests). Zero derives it from the model and max_tokens.
	Timeout time.Duration
	// Caps are what each model accepts (capabilities.go); nil uses the embedded catalogue's.
	Caps Capabilities
}

// anthropicClient is the Client over anthropic-sdk-go. It does not retry (MaxRetries 0): retries live in limited.
type anthropicClient struct {
	sdk       anthropic.Client
	gateway   bool
	workspace string
	stage     string
	timeout   time.Duration
	// caps map each request to what its model accepts; capsErr is why there are none (every call is then refused).
	caps    Capabilities
	capsErr error
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
	c.timeout = cfg.Timeout
	c.caps = cfg.Caps
	if c.caps == nil {
		c.caps, c.capsErr = DefaultCapabilities()
	}
	// Explicit options only: the SDK's own environment defaults (ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL ...) must not
	// decide which key or endpoint is used.
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(0),
		option.WithHTTPClient(trackedClient(cfg.HTTPClient)),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
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
	req, err := c.adapt(req)
	if err != nil {
		return Response{}, err
	}
	params, err := toMessageParams(req)
	if err != nil {
		return Response{}, err
	}
	var raw *http.Response
	timeout := c.timeout
	if timeout <= 0 {
		timeout = requestTimeout(req.Model, req.MaxTokens)
	}
	st := &callState{}
	ctx = context.WithValue(ctx, callStateKey{}, st)
	opts := append(c.requestOptions(req.Run), option.WithResponseInto(&raw), option.WithRequestTimeout(timeout))
	msg, err := c.sdk.Messages.New(ctx, params, opts...)
	if err != nil {
		return Response{}, c.convertError(err, st.sent.Load())
	}
	resp := fromMessage(msg)
	if raw != nil {
		resp.RequestID = raw.Header.Get("request-id")
	}
	return resp, nil
}

// CountTokens implements Client.
func (c *anthropicClient) CountTokens(ctx context.Context, req Request) (int64, error) {
	req, err := c.adapt(req)
	if err != nil {
		return 0, err
	}
	params, err := toCountParams(req)
	if err != nil {
		return 0, err
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = countTokensTimeout
	}
	st := &callState{}
	ctx = context.WithValue(ctx, callStateKey{}, st)
	res, err := c.sdk.Messages.CountTokens(ctx, params, append(c.requestOptions(req.Run), option.WithRequestTimeout(timeout))...)
	if err != nil {
		return 0, c.convertError(err, st.sent.Load())
	}
	return res.InputTokens, nil
}

// adapt maps the request to what its model accepts (capabilities.go). count_tokens takes the same mapping: it refuses what Messages
// refuses.
func (c *anthropicClient) adapt(req Request) (Request, error) {
	if c.capsErr != nil {
		return req, fmt.Errorf("llm: the model capabilities could not be read: %w", c.capsErr)
	}
	return c.caps.adapt(req)
}

// convertError turns an SDK error into *APIError (an answer from the server: the request was not processed) or a
// *TransportError (anything below HTTP, with sent saying whether the request reached the wire).
func (c *anthropicClient) convertError(err error, sent bool) error {
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
	// The message has secrets removed. Unwrap keeps errors.Is(err, context.Canceled) and friends working; the wrapped
	// value is not printed by this type.
	where := "before the request was sent"
	if sent {
		where = "after the request was sent"
	}
	return &TransportError{Sent: sent, Err: err, msg: "llm: request failed " + where + ": " + scrub(err.Error(), c.secrets)}
}

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
	// A block this package does not model comes back as the raw JSON the API sent (server_tool_use,
	// web_search_tool_result ...), so the assistant turn can be appended to the conversation unchanged.
	if len(b.Raw) > 0 {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b.Raw, &probe) != nil || probe.Type == "" || probe.Type != string(b.Type) {
			return anthropic.ContentBlockParamUnion{}, fmt.Errorf("llm: raw block of type %q is not a JSON object of that type", b.Type)
		}
		var u anthropic.ContentBlockParamUnion
		param.SetJSON(b.Raw, &u)
		return u, nil
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
	case ThinkingBetweenTools:
		// The type and nothing else: display or a budget next to it is a 400.
		b := anthropic.NewThinkingConfigBetweenToolsParam()
		return anthropic.ThinkingConfigParamUnion{OfBetweenTools: &b}, nil
	}
	return anthropic.ThinkingConfigParamUnion{}, fmt.Errorf("llm: thinking mode %q is not adaptive, enabled or disabled", t.Mode)
}

func toToolChoice(tc *ToolChoice) (anthropic.ToolChoiceUnionParam, error) {
	if tc == nil || tc.Type == "" {
		return anthropic.ToolChoiceUnionParam{}, nil
	}
	var dp param.Opt[bool]
	if tc.DisableParallelToolUse {
		dp = anthropic.Bool(true)
	}
	switch tc.Type {
	case "auto":
		return anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{DisableParallelToolUse: dp}}, nil
	case "any":
		return anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{DisableParallelToolUse: dp}}, nil
	case "none":
		return anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}, nil
	case "tool":
		if tc.Name == "" {
			return anthropic.ToolChoiceUnionParam{}, errors.New("llm: tool_choice tool needs a name")
		}
		return anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: tc.Name, DisableParallelToolUse: dp}}, nil
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
			// Server tool blocks (server_tool_use, web_search_tool_result ...) and anything newer: keep the JSON so the
			// turn can be sent back unchanged.
			r.Content = append(r.Content, Block{Type: BlockType(b.Type), Raw: json.RawMessage(b.RawJSON())})
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
	// split (it is absent, or does not add up to the total), say so: the cost is then worked out from the request's
	// own breakpoints (metered.settleUsage), never silently at the cheaper 5-minute rate.
	split := u.CacheCreation.Ephemeral1hInputTokens + u.CacheCreation.Ephemeral5mInputTokens
	if out.CacheWriteTokens > 0 && split != out.CacheWriteTokens {
		out.CacheWriteSplitMissing = true
		return out
	}
	out.CacheWrite1hTokens = min(u.CacheCreation.Ephemeral1hInputTokens, out.CacheWriteTokens)
	return out
}
