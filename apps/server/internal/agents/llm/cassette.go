package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Record and replay (LLM_MODE, docs/design/agent-runtime-v1.md section 2).
//
// The cassette key is the SHA-256 of the canonical request: the model, system blocks, messages, tools and the
// parameters, as JSON with sorted keys, where an image is replaced by the SHA-256 of its bytes. The run, the
// profile and the budget are not part of it, so the same request replays whatever run made it. A cassette lives at
// <dir>/<stage>/<key>.json and holds a redacted request summary, the response and the usage. It never holds the API
// key, a header or a base URL: nothing but the request's own fields and the response is ever written.

const (
	cassetteVersion = 1
	// keyVersion is hashed into every key, so a change of the canonical form invalidates old cassettes loudly (a
	// replay miss) instead of matching them wrongly.
	keyVersion = 1
	// imageTokenEstimate is the replay-mode stand-in for what an image costs in tokens.
	imageTokenEstimate = 1600
)

var stageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// canonicalRequest returns the canonical JSON of the request and the number of images in it.
func canonicalRequest(req Request) (canon []byte, images int, err error) {
	system, n1, err := canonBlocks(req.System)
	if err != nil {
		return nil, 0, err
	}
	var msgs []any
	images = n1
	for _, m := range req.Messages {
		blocks, n, err := canonBlocks(m.Content)
		if err != nil {
			return nil, 0, err
		}
		images += n
		msgs = append(msgs, map[string]any{"role": string(m.Role), "content": blocks})
	}
	var tools []any
	for _, t := range req.Tools {
		schema, err := canonRaw(t.InputSchema)
		if err != nil {
			return nil, 0, fmt.Errorf("llm: tool %q schema: %w", t.Name, err)
		}
		tools = append(tools, map[string]any{
			"name": t.Name, "description": t.Description, "input_schema": schema, "strict": t.Strict, "cache": canonCache(t.Cache),
		})
	}
	doc := map[string]any{
		"v": keyVersion, "model": req.Model, "system": system, "messages": msgs, "tools": tools,
		"max_tokens": req.MaxTokens, "effort": req.Effort, "stop_sequences": req.StopSequences,
	}
	if req.ToolChoice != nil {
		doc["tool_choice"] = map[string]any{"type": req.ToolChoice.Type, "name": req.ToolChoice.Name}
	}
	if req.Thinking != nil {
		doc["thinking"] = map[string]any{"mode": string(req.Thinking.Mode), "budget_tokens": req.Thinking.BudgetTokens, "display": req.Thinking.Display}
	}
	canon, err = json.Marshal(doc) // map keys are sorted by encoding/json
	return canon, images, err
}

func canonCache(c *CacheControl) any {
	if c == nil {
		return nil
	}
	if c.TTL == "" {
		return "5m"
	}
	return c.TTL
}

// canonRaw re-encodes raw JSON with sorted keys and exact numbers.
func canonRaw(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func canonBlocks(blocks []Block) (out []any, images int, err error) {
	for _, b := range blocks {
		m := map[string]any{"type": string(b.Type), "cache": canonCache(b.Cache)}
		switch b.Type {
		case BlockText:
			m["text"] = b.Text
		case BlockImage:
			if b.Image == nil || len(b.Image.Data) == 0 {
				return nil, 0, errors.New("llm: an image must have its bytes (resolve references first)")
			}
			sum := sha256.Sum256(b.Image.Data)
			m["image_sha256"], m["media_type"] = hex.EncodeToString(sum[:]), b.Image.MediaType
			images++
		case BlockToolUse:
			in, err := canonRaw(b.Input)
			if err != nil {
				return nil, 0, fmt.Errorf("llm: tool_use input: %w", err)
			}
			m["id"], m["name"], m["input"] = b.ID, b.Name, in
		case BlockToolResult:
			inner, n, err := canonBlocks(b.Content)
			if err != nil {
				return nil, 0, err
			}
			images += n
			m["tool_use_id"], m["is_error"], m["content"] = b.ToolUseID, b.IsError, inner
		case BlockThinking:
			m["thinking"], m["signature"] = b.Thinking, b.Signature
		case BlockRedactedThinking:
			m["data"] = b.Data
		default:
			return nil, 0, fmt.Errorf("llm: unknown block type %q", b.Type)
		}
		out = append(out, m)
	}
	return out, images, nil
}

// RequestKey is the cassette key of a request: the hex SHA-256 of its canonical form.
func RequestKey(req Request) (string, error) {
	canon, _, err := canonicalRequest(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// EstimateTokens is a local, free stand-in for CountTokens (replay mode has no API to ask): about three bytes per
// token over the request's canonical JSON, and a fixed amount per image. It leans high, because it only feeds caps.
func EstimateTokens(req Request) (int64, error) {
	canon, images, err := canonicalRequest(req)
	if err != nil {
		return 0, err
	}
	return int64(len(canon))/3 + int64(images)*imageTokenEstimate, nil
}

// ---- file format ---------------------------------------------------------------------------------------------

type cassetteFile struct {
	Version    int              `json:"version"`
	Stage      string           `json:"stage"`
	Key        string           `json:"key"`
	RecordedAt time.Time        `json:"recorded_at"`
	Request    cassetteSummary  `json:"request"`
	Response   cassetteResponse `json:"response"`
}

// cassetteSummary says what was asked without storing the prompt, so a cassette can be told apart from a near miss
// without reading user text. Hashes and counts only.
type cassetteSummary struct {
	Model        string         `json:"model"`
	MaxTokens    int            `json:"max_tokens"`
	Effort       string         `json:"effort,omitempty"`
	Thinking     string         `json:"thinking,omitempty"`
	SystemBlocks int            `json:"system_blocks"`
	Messages     []summaryMsg   `json:"messages"`
	Tools        []string       `json:"tools,omitempty"`
	Images       []summaryImage `json:"images,omitempty"`
}

type summaryMsg struct {
	Role   string   `json:"role"`
	Blocks []string `json:"blocks"`
}

type summaryImage struct {
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
	Bytes     int    `json:"bytes"`
}

type cassetteBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
}

type cassetteResponse struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    []cassetteBlock `json:"content"`
	Usage      Usage           `json:"usage"`
}

func summarize(req Request) cassetteSummary {
	s := cassetteSummary{Model: req.Model, MaxTokens: req.MaxTokens, Effort: req.Effort, SystemBlocks: len(req.System)}
	if req.Thinking != nil {
		s.Thinking = string(req.Thinking.Mode)
	}
	var walk func(blocks []Block) []string
	walk = func(blocks []Block) []string {
		var kinds []string
		for _, b := range blocks {
			kinds = append(kinds, string(b.Type))
			if b.Type == BlockImage && b.Image != nil {
				sum := sha256.Sum256(b.Image.Data)
				s.Images = append(s.Images, summaryImage{SHA256: hex.EncodeToString(sum[:]), MediaType: b.Image.MediaType, Bytes: len(b.Image.Data)})
			}
			if b.Type == BlockToolResult {
				walk(b.Content)
			}
		}
		return kinds
	}
	walk(req.System)
	for _, m := range req.Messages {
		s.Messages = append(s.Messages, summaryMsg{Role: string(m.Role), Blocks: walk(m.Content)})
	}
	for _, t := range req.Tools {
		s.Tools = append(s.Tools, t.Name)
	}
	return s
}

func toCassetteResponse(r Response) cassetteResponse {
	out := cassetteResponse{ID: r.ID, Model: r.Model, StopReason: r.StopReason, Usage: r.Usage}
	for _, b := range r.Content {
		out.Content = append(out.Content, cassetteBlock{Type: string(b.Type), Text: b.Text, ID: b.ID, Name: b.Name, Input: b.Input, Thinking: b.Thinking, Signature: b.Signature, Data: b.Data})
	}
	return out
}

func (c cassetteResponse) response() Response {
	r := Response{ID: c.ID, Model: c.Model, StopReason: c.StopReason, Usage: c.Usage}
	for _, b := range c.Content {
		in := b.Input
		// The file is indented, which re-indents raw JSON too; hand the model's own compact form back.
		var buf bytes.Buffer
		if len(in) > 0 && json.Compact(&buf, in) == nil {
			in = json.RawMessage(buf.Bytes())
		}
		r.Content = append(r.Content, Block{Type: BlockType(b.Type), Text: b.Text, ID: b.ID, Name: b.Name, Input: in, Thinking: b.Thinking, Signature: b.Signature, Data: b.Data})
	}
	return r
}

// ---- the client ----------------------------------------------------------------------------------------------

// cassetteClient applies LLM_MODE. In live mode it is not built at all. Its inner client is nil in replay mode, which
// is how replay is guaranteed never to reach the API.
type cassetteClient struct {
	inner Client // nil when replaying
	mode  Mode
	dir   string
	stage string
	now   func() time.Time
	log   *slog.Logger
}

func newCassetteClient(inner Client, mode Mode, dir, stage string, log *slog.Logger) (*cassetteClient, error) {
	if !stageRE.MatchString(stage) {
		return nil, fmt.Errorf("llm: stage %q is not a plain name (lowercase letters, digits, dot, dash, underscore)", stage)
	}
	if mode == ModeReplay {
		inner = nil
	}
	return &cassetteClient{inner: inner, mode: mode, dir: dir, stage: stage, now: time.Now, log: log}, nil
}

func (c *cassetteClient) path(key string) string { return filepath.Join(c.dir, c.stage, key+".json") }

// Messages implements Client.
func (c *cassetteClient) Messages(ctx context.Context, req Request) (Response, error) {
	key, err := RequestKey(req)
	if err != nil {
		return Response{}, err
	}
	if c.mode == ModeReplay {
		return c.replay(key)
	}
	resp, err := c.inner.Messages(ctx, req)
	if err != nil {
		return resp, err
	}
	if werr := c.record(key, req, resp); werr != nil {
		// The call is paid for; hand the response over, and say the cassette is missing.
		c.log.Error("llm: could not write the cassette", "stage", c.stage, "key", key, "error", werr)
		return resp, fmt.Errorf("llm: record: write cassette %s: %w", key, werr)
	}
	return resp, nil
}

// CountTokens implements Client. Replay has no API to ask, so it estimates locally.
func (c *cassetteClient) CountTokens(ctx context.Context, req Request) (int64, error) {
	if c.mode == ModeReplay {
		return EstimateTokens(req)
	}
	return c.inner.CountTokens(ctx, req)
}

func (c *cassetteClient) replay(key string) (Response, error) {
	p := c.path(key)
	data, err := os.ReadFile(p) //nolint:gosec // G304: dir is test configuration, stage and key are validated name and hex
	if errors.Is(err, os.ErrNotExist) {
		return Response{}, &ReplayMissError{Stage: c.stage, Key: key, Path: p}
	}
	if err != nil {
		return Response{}, fmt.Errorf("llm: replay: read cassette %s: %w", key, err)
	}
	var f cassetteFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Response{}, fmt.Errorf("llm: replay: cassette %s is not valid JSON: %w", key, err)
	}
	if f.Version != cassetteVersion || f.Key != key {
		return Response{}, fmt.Errorf("llm: replay: cassette %s has version %d and key %q (want version %d); re-record it", key, f.Version, f.Key, cassetteVersion)
	}
	return f.Response.response(), nil
}

func (c *cassetteClient) record(key string, req Request, resp Response) error {
	f := cassetteFile{Version: cassetteVersion, Stage: c.stage, Key: key, RecordedAt: c.now().UTC().Truncate(time.Second), Request: summarize(req), Response: toCassetteResponse(resp)}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(c.dir, c.stage)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cassette-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after a successful rename
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path(key))
}
