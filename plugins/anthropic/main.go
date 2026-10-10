// Command anthropic is the provider plugin for the Anthropic Messages API.
//
// Research notes, taken from the Anthropic documentation on 2026-10-11:
//
// Models (https://platform.claude.com/docs/en/about-claude/models/overview.md
// and each model page under https://platform.claude.com/docs/en/models/):
//   - Current: claude-fable-5-1, claude-opus-5-5, claude-sonnet-5-5,
//     claude-haiku-5-5. Legacy, still available: claude-fable-5,
//     claude-opus-5, claude-opus-4-8, claude-opus-4-7, claude-opus-4-6,
//     claude-sonnet-5, claude-sonnet-4-6, claude-opus-4-5, claude-haiku-4-5.
//   - Every model in the table below: 1M-token context window and 128K max
//     output on the synchronous Messages API. Claude Opus 4.5 and Haiku 4.5
//     are left out: they support only the deprecated budget_tokens thinking
//     mode (Haiku 4.5 also has no effort parameter and a 200K window).
//     Claude Mythos models need a trusted access program and are left out.
//
// Messages API (https://platform.claude.com/docs/en/api/messages/create.md,
// https://platform.claude.com/docs/en/api/versioning.md,
// https://platform.claude.com/docs/en/api/errors.md):
//   - POST /v1/messages with headers x-api-key and anthropic-version:
//     2023-06-01 (the only current version). Required body fields: model,
//     max_tokens, messages.
//   - system is a string or a list of {type:"text", text, cache_control}.
//   - Response content blocks include text, thinking {thinking, signature},
//     redacted_thinking {data} and tool_use {id, name, input}. usage carries
//     input_tokens, output_tokens, cache_read_input_tokens and
//     cache_creation_input_tokens; their sum of inputs is the total input.
//   - Errors: {"type":"error","error":{"type","message"},"request_id"}.
//   - Thinking and redacted_thinking blocks must be passed back unmodified
//     and in order. On Fable 5.1, Opus 5.5, Sonnet 5.5 and Haiku 5.5 a
//     replayed thinking block is also bound to the unchanged history before
//     it, so the history must stay append-only.
//
// Tool use (https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview.md):
//   - tool_choice {type:"auto", disable_parallel_tool_use:true} gives at most
//     one tool call per response. Forced tool_choice (any/tool) returns 400 on
//     Opus 5.5, Sonnet 5.5 and Fable 5.1, so auto is used everywhere.
//   - Results go back as a user block {type:"tool_result", tool_use_id,
//     content}.
//
// Prompt caching (https://platform.claude.com/docs/en/build-with-claude/prompt-caching.md):
//   - cache_control: {"type":"ephemeral"} (5-minute default; "ttl":"1h"
//     optional). At most 4 breakpoints per request. Allowed on tools, system
//     text blocks and message content blocks (text, tool_use, tool_result);
//     not on thinking blocks or empty text blocks. This plugin uses two: the
//     last system block and the last cacheable block of the final message.
//
// Thinking and effort (https://platform.claude.com/docs/en/build-with-claude/effort.md,
// https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting.md,
// https://platform.claude.com/docs/en/build-with-claude/thinking-steering-and-cost.md):
//   - Effort is output_config.effort, one of low, medium, high, xhigh, max.
//     xhigh is not available on Opus 4.6 or Sonnet 4.6. Default is medium on
//     Opus 5.5 and Haiku 5.5, high elsewhere.
//   - thinking.type per model: Fable 5.1, Fable 5 and Opus 5.5 are adaptive
//     only and always on (enabled and disabled return 400). Opus 5, Sonnet 5,
//     Haiku 5.5, Opus 4.8, Opus 4.7, Opus 4.6 and Sonnet 4.6 accept adaptive
//     and disabled (Opus 5 and Haiku 5.5 only at effort high or below;
//     Opus 4.8/4.7/4.6 and Sonnet 4.6 run without thinking when it is
//     omitted). Sonnet 5.5 accepts adaptive and between_tools (effort high or
//     below) and rejects disabled. budget_tokens is rejected on 4.7 and later.
//   - max_tokens is a hard cap on thinking plus text. The effort page
//     recommends starting at 64k at high effort levels; the errors page warns
//     that large non-streaming requests need TCP keep-alive, which Go's
//     default transport sets. The default max_tokens is therefore 64000.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/SEAN-7/bot/internal/proto"
)

const (
	anthropicVersion    = "2023-06-01"
	defaultBaseURL      = "https://api.anthropic.com"
	maxCacheBreakpoints = 4
)

type modelSpec struct {
	id              string
	contextTokens   int
	maxOutput       int
	defaultMax      int
	efforts         []string
	defaultEffort   string
	thinking        []string // accepted thinking.type values; nil when not configurable
	thinkingDefault string
}

var (
	allEfforts  = []string{"low", "medium", "high", "xhigh", "max"}
	noXhigh     = []string{"low", "medium", "high", "max"}
	disableable = []string{"adaptive", "disabled"}
)

var models = []modelSpec{
	{id: "claude-opus-5-5", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "medium"},
	{id: "claude-fable-5-1", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high"},
	{id: "claude-sonnet-5-5", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high", thinking: []string{"adaptive", "between_tools"}, thinkingDefault: "adaptive"},
	{id: "claude-haiku-5-5", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "medium", thinking: disableable, thinkingDefault: "adaptive"},
	{id: "claude-fable-5", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high"},
	{id: "claude-opus-5", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high", thinking: disableable, thinkingDefault: "adaptive"},
	{id: "claude-sonnet-5", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high", thinking: disableable, thinkingDefault: "adaptive"},
	{id: "claude-opus-4-8", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high", thinking: disableable, thinkingDefault: "disabled"},
	{id: "claude-opus-4-7", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: allEfforts, defaultEffort: "high", thinking: disableable, thinkingDefault: "disabled"},
	{id: "claude-opus-4-6", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: noXhigh, defaultEffort: "high", thinking: disableable, thinkingDefault: "disabled"},
	{id: "claude-sonnet-4-6", contextTokens: 1000000, maxOutput: 128000, defaultMax: 64000, efforts: noXhigh, defaultEffort: "high", thinking: disableable, thinkingDefault: "disabled"},
}

func findModel(id string) (modelSpec, bool) {
	for _, m := range models {
		if m.id == id {
			return m, true
		}
	}
	return modelSpec{}, false
}

type handler struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func main() {
	proto.Serve(handler{
		apiKey:  os.Getenv("ANTHROPIC_API_KEY"),
		baseURL: os.Getenv("ANTHROPIC_BASE_URL"),
	})
}

func (handler) Describe() proto.DescribeResponse {
	resp := proto.DescribeResponse{
		Settings: []proto.Setting{
			{Name: "ANTHROPIC_API_KEY", Description: "Anthropic API key", Secret: true},
			{Name: "ANTHROPIC_BASE_URL", Description: "Optional API base URL (default " + defaultBaseURL + ")"},
		},
	}
	for _, m := range models {
		one, maxOut := 1, m.maxOutput
		opts := map[string]proto.Option{
			"max_tokens": {
				Description: fmt.Sprintf("Maximum output tokens, thinking included (default %d)", m.defaultMax),
				Type:        "integer", Min: &one, Max: &maxOut,
			},
			"effort": {
				Description: "Effort level, sent as output_config.effort (default " + m.defaultEffort + ")",
				Values:      anyList(m.efforts),
			},
		}
		if m.thinking != nil {
			opts["thinking"] = proto.Option{
				Description: "Thinking mode, sent as thinking.type (default " + m.thinkingDefault + ")",
				Values:      anyList(m.thinking),
			}
		}
		resp.Models = append(resp.Models, proto.Model{ID: m.id, ContextTokens: m.contextTokens, Options: opts})
	}
	return resp
}

func anyList(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// Request types for the Messages API.

type cacheControl struct {
	Type string `json:"type"`
}

var ephemeral = &cacheControl{Type: "ephemeral"}

type textBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type toolUseBlock struct {
	Type  string            `json:"type"`
	ID    string            `json:"id"`
	Name  string            `json:"name"`
	Input map[string]string `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type apiMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type                   string `json:"type"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
}

type thinkingConfig struct {
	Type string `json:"type"`
}

type outputConfig struct {
	Effort string `json:"effort"`
}

type apiRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	System       []textBlock     `json:"system,omitempty"`
	Messages     []apiMessage    `json:"messages"`
	Tools        []apiTool       `json:"tools,omitempty"`
	ToolChoice   *toolChoice     `json:"tool_choice,omitempty"`
	Thinking     *thinkingConfig `json:"thinking,omitempty"`
	OutputConfig *outputConfig   `json:"output_config,omitempty"`
}

// Response types.

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type apiResponse struct {
	Content []json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func fail(format string, args ...any) proto.CompleteResponse {
	return proto.CompleteResponse{Error: &proto.Error{Message: fmt.Sprintf(format, args...)}}
}

func (h handler) Complete(ctx context.Context, req proto.CompleteRequest) proto.CompleteResponse {
	if h.apiKey == "" {
		return fail("ANTHROPIC_API_KEY is not set")
	}
	body, err := buildRequest(req)
	if err != nil {
		return fail("%v", err)
	}
	data, err := marshal(body)
	if err != nil {
		return fail("encode request: %v", err)
	}
	base := strings.TrimRight(h.baseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return fail("build request: %v", err)
	}
	httpReq.Header.Set("x-api-key", h.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")
	client := h.client
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return fail("anthropic request failed: %v", err)
	}
	defer httpResp.Body.Close()
	respData, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fail("anthropic: HTTP %d: read body: %v", httpResp.StatusCode, err)
	}
	if httpResp.StatusCode/100 != 2 {
		var e apiError
		if json.Unmarshal(respData, &e) == nil && e.Error.Message != "" {
			return fail("anthropic: HTTP %d %s: %s", httpResp.StatusCode, e.Error.Type, e.Error.Message)
		}
		return fail("anthropic: HTTP %d: %s", httpResp.StatusCode, clip(string(respData), 2000))
	}
	return parseResponse(respData)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func buildRequest(req proto.CompleteRequest) (apiRequest, error) {
	out := apiRequest{
		Model:      req.Model,
		ToolChoice: &toolChoice{Type: "auto", DisableParallelToolUse: true},
	}
	if m, ok := findModel(req.Model); ok {
		out.MaxTokens = m.defaultMax
	} else {
		out.MaxTokens = 64000
	}
	for name, v := range req.Options {
		switch name {
		case "max_tokens":
			n, ok := v.(float64)
			if !ok || n != float64(int(n)) || n < 1 {
				return out, fmt.Errorf("option max_tokens must be a positive integer, got %v", v)
			}
			out.MaxTokens = int(n)
		case "effort":
			s, ok := v.(string)
			if !ok {
				return out, fmt.Errorf("option effort must be a string, got %v", v)
			}
			out.OutputConfig = &outputConfig{Effort: s}
		case "thinking":
			s, ok := v.(string)
			if !ok {
				return out, fmt.Errorf("option thinking must be a string, got %v", v)
			}
			out.Thinking = &thinkingConfig{Type: s}
		default:
			return out, fmt.Errorf("unknown option %q", name)
		}
	}

	for _, seg := range req.System {
		if seg.Text != "" {
			out.System = append(out.System, textBlock{Type: "text", Text: seg.Text})
		}
	}
	if n := len(out.System); n > 0 {
		out.System[n-1].CacheControl = ephemeral
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, apiTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}

	msgs, err := buildMessages(req.Messages)
	if err != nil {
		return out, err
	}
	if n := len(msgs); n > 0 {
		markLastCacheable(msgs[n-1].Content)
	}
	out.Messages = msgs
	return out, nil
}

func buildMessages(in []proto.Message) ([]apiMessage, error) {
	var out []apiMessage
	// Tool results must answer the tool_use id the API saw. For replayed
	// opaque content that is the provider's id, not the daemon's call_id.
	toolIDs := map[string]string{}
	add := func(role string, blocks ...json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, apiMessage{Role: role, Content: blocks})
	}
	for i, m := range in {
		switch m.Role {
		case "user":
			if m.Content != "" {
				add("user", mustJSON(textBlock{Type: "text", Text: m.Content}))
			}
		case "assistant":
			if blocks, ok := opaqueBlocks(m.Opaque); ok {
				if id := firstToolUseID(blocks); id != "" && m.CallID != "" {
					toolIDs[m.CallID] = id
				}
				add("assistant", blocks...)
				continue
			}
			var blocks []json.RawMessage
			if m.Text != "" {
				blocks = append(blocks, mustJSON(textBlock{Type: "text", Text: m.Text}))
			}
			if m.Command != nil {
				blocks = append(blocks, mustJSON(toolUseBlock{
					Type: "tool_use", ID: m.CallID, Name: "sh",
					Input: map[string]string{"command": *m.Command},
				}))
			}
			add("assistant", blocks...)
		case "tool":
			id := m.CallID
			if mapped, ok := toolIDs[id]; ok {
				id = mapped
			}
			add("user", mustJSON(toolResultBlock{Type: "tool_result", ToolUseID: id, Content: m.Content}))
		default:
			return nil, fmt.Errorf("message %d: unknown role %q", i, m.Role)
		}
	}
	return out, nil
}

func opaqueBlocks(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		return nil, false
	}
	return blocks, true
}

func firstToolUseID(blocks []json.RawMessage) string {
	for _, b := range blocks {
		var cb contentBlock
		if json.Unmarshal(b, &cb) == nil && cb.Type == "tool_use" {
			return cb.ID
		}
	}
	return ""
}

// markLastCacheable puts a cache breakpoint on the last block that may carry
// one: not a thinking block and not an empty text block.
func markLastCacheable(blocks []json.RawMessage) {
	for i := len(blocks) - 1; i >= 0; i-- {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(blocks[i], &obj); err != nil {
			continue
		}
		var typ, text string
		json.Unmarshal(obj["type"], &typ)
		if typ == "thinking" || typ == "redacted_thinking" {
			continue
		}
		if typ == "text" {
			if json.Unmarshal(obj["text"], &text); text == "" {
				continue
			}
		}
		obj["cache_control"] = mustJSON(ephemeral)
		blocks[i] = mustJSON(obj)
		return
	}
}

// marshal encodes without HTML escaping so replayed text stays byte-faithful.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func mustJSON(v any) json.RawMessage {
	data, err := marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func parseResponse(data []byte) proto.CompleteResponse {
	var r apiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return fail("anthropic: decode response: %v", err)
	}
	out := proto.CompleteResponse{
		Usage: proto.Usage{
			InputTokens:      r.Usage.InputTokens,
			OutputTokens:     r.Usage.OutputTokens,
			CacheReadTokens:  r.Usage.CacheReadInputTokens,
			CacheWriteTokens: r.Usage.CacheCreationInputTokens,
		},
	}
	var texts []string
	for _, raw := range r.Content {
		var b contentBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return fail("anthropic: decode content block: %v", err)
		}
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "tool_use":
			if out.Command != nil {
				continue
			}
			var input struct {
				Command *string `json:"command"`
			}
			if err := json.Unmarshal(b.Input, &input); err != nil || input.Command == nil {
				return fail("anthropic: tool_use %s has no string command: %s", b.Name, b.Input)
			}
			out.Command = input.Command
		}
	}
	out.Text = strings.Join(texts, "\n\n")
	if len(r.Content) > 0 {
		out.Opaque = mustJSON(r.Content)
	}
	return out
}
