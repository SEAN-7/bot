// Command openai-compatible is a provider plugin for OpenAI-compatible Chat
// Completions endpoints (OpenAI, vLLM, Ollama, OpenRouter, llama.cpp and
// others), speaking protocol version 1 from internal/proto.
//
// API research, 2026-10-11. platform.openai.com/docs/api-reference returned
// HTTP 403 to automated fetches, so the field definitions below were taken from
// the OpenAI Python SDK types, which are generated from OpenAI's OpenAPI spec
// (main branch, fetched 2026-10-11), and from OpenAI's guides at
// developers.openai.com:
//
//   - https://github.com/openai/openai-python/blob/main/src/openai/types/chat/completion_create_params.py
//   - https://github.com/openai/openai-python/blob/main/src/openai/types/shared/reasoning_effort.py
//   - https://github.com/openai/openai-python/blob/main/src/openai/types/completion_usage.py
//   - https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_assistant_message_param.py
//   - https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_tool_message_param.py
//   - https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_message_function_tool_call.py
//   - https://developers.openai.com/api/docs/guides/function-calling
//   - https://developers.openai.com/api/docs/guides/reasoning
//   - https://docs.ollama.com/api/openai-compatibility
//   - https://openrouter.ai/docs/api/reference/parameters
//
// Findings:
//
//   - Tools: tools: [{"type":"function","function":{"name","description",
//     "parameters"}}], where parameters is a JSON Schema object. "strict" is
//     optional; Chat Completions is non-strict by default. It is not sent,
//     because strict mode needs additionalProperties:false and is not
//     universally supported by compatible servers.
//   - Response tool calls: choices[0].message.tool_calls: [{"id","type":
//     "function","function":{"name","arguments"}}]. arguments is a string of
//     JSON that the model may get wrong, so it is validated. Some compatible
//     servers return arguments as an object, which is also accepted.
//   - Assistant message: {"role":"assistant","content","tool_calls"}. content
//     is "Required unless tool_calls or function_call is specified" and may
//     be null.
//   - Tool message: {"role":"tool","tool_call_id","content"}, all required.
//   - parallel_tool_calls (bool, default true): "Whether to enable parallel
//     function calling during tool use". Always sent as false. Ollama does not
//     document it and ignores unknown fields, and the plugin uses only the
//     first tool call either way.
//   - reasoning_effort (top level in Chat Completions; reasoning.effort is the
//     Responses API form): "Currently supported values are none, minimal,
//     low, medium, high, xhigh, and max ... Not all reasoning models support
//     every value." The guide adds that some current models reject none and
//     minimal. OpenRouter documents none, minimal, low, medium, high and
//     xhigh. Ollama maps names per model. The option exposes low, medium and
//     high, the values that every source accepts, and the field is sent only
//     when the option is set.
//   - max_tokens vs max_completion_tokens: OpenAI documents max_tokens as
//     "deprecated in favor of max_completion_tokens, and ... not compatible
//     with o-series models". max_completion_tokens is "An upper bound ...
//     including visible output tokens and reasoning tokens". Ollama documents
//     only max_tokens. vLLM and OpenRouter accept both. The plugin sends
//     max_tokens, the name every compatible server accepts, and only when the
//     option is set. On OpenAI's own reasoning models, leave the option unset;
//     setting it gives a visible 400 rather than being silently ignored.
//   - Usage: usage.prompt_tokens, usage.completion_tokens, usage.total_tokens,
//     and usage.prompt_tokens_details.cached_tokens ("Cached tokens present in
//     the prompt", included in prompt_tokens). Input is reported as
//     prompt_tokens - cached_tokens, cache read as cached_tokens, and output
//     as completion_tokens, which includes reasoning tokens. The SDK also
//     lists prompt_tokens_details.cache_write_tokens ("The unadjusted number
//     of prompt tokens written to cache"). It is not mapped, because its
//     relation to prompt_tokens is undocumented and cache_write_tokens stays 0.
//   - Errors: non-2xx responses carry {"error":{"message",...}}. Some
//     compatible servers also return an error object with status 200.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/SEAN-7/bot/internal/proto"
)

const defaultBaseURL = "https://api.openai.com/v1"

type handler struct {
	getenv func(string) string
	client *http.Client
}

func main() {
	proto.Serve(handler{getenv: os.Getenv, client: http.DefaultClient})
}

func intp(n int) *int { return &n }

var settings = []proto.Setting{
	{Name: "OPENAI_BASE_URL", Description: "Base URL of the OpenAI-compatible API, up to and including /v1 (default " + defaultBaseURL + "). Requests go to <base>/chat/completions."},
	{Name: "OPENAI_API_KEY", Description: "API key, sent as a Bearer token. Leave empty for servers that need none.", Secret: true},
	{Name: "OPENAI_MODELS", Description: "Comma-separated model ids, each optionally with its context size in tokens, for example gpt-5=400000,llama3. Compatible endpoints do not report context sizes; a size given here is shown to agents choosing a model."},
}

func modelOptions() map[string]proto.Option {
	return map[string]proto.Option{
		"max_tokens": {
			Description: "Maximum tokens to generate, sent as max_tokens. Leave unset for OpenAI reasoning models, which accept only max_completion_tokens.",
			Type:        "integer",
			Min:         intp(1),
			Max:         intp(1000000),
		},
		"reasoning_effort": {
			Description: "Reasoning effort for reasoning models, sent as reasoning_effort.",
			Values:      []any{"low", "medium", "high"},
		},
	}
}

func (h handler) Describe() proto.DescribeResponse {
	d := proto.DescribeResponse{Settings: settings, Models: []proto.Model{}}
	models, err := parseModels(h.getenv("OPENAI_MODELS"))
	if err != nil {
		d.Error = &proto.Error{Message: err.Error()}
		return d
	}
	d.Models = models
	return d
}

func parseModels(s string) ([]proto.Model, error) {
	models := []proto.Model{}
	if strings.TrimSpace(s) == "" {
		return models, nil
	}
	seen := map[string]bool{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		id, size, sized := strings.Cut(pair, "=")
		id, size = strings.TrimSpace(id), strings.TrimSpace(size)
		if id == "" {
			return nil, fmt.Errorf("OPENAI_MODELS: %q has no model id", pair)
		}
		n := 0
		if sized {
			var err error
			if n, err = strconv.Atoi(size); err != nil || n <= 0 {
				return nil, fmt.Errorf("OPENAI_MODELS: context_tokens for %q must be a positive integer, got %q", id, size)
			}
		}
		if seen[id] {
			return nil, fmt.Errorf("OPENAI_MODELS: model %q listed twice", id)
		}
		seen[id] = true
		models = append(models, proto.Model{ID: id, ContextTokens: n, Options: modelOptions()})
	}
	return models, nil
}

// Wire types.

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type responseMessage struct {
	Content   json.RawMessage `json:"content"`
	Refusal   string          `json:"refusal"`
	ToolCalls []toolCall      `json:"tool_calls"`
}

type apiError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
}

// overflow recognises "request exceeds the context window" errors, which a
// retry cannot fix, from OpenAI and common compatible servers.
func (e *apiError) overflow() bool {
	m := strings.ToLower(e.Message)
	return e.Code == "context_length_exceeded" || strings.Contains(m, "maximum context length") || strings.Contains(m, "context length exceeded")
}

type chatResponse struct {
	Choices []struct {
		Message json.RawMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *apiError `json:"error"`
}

func fail(format string, args ...any) proto.CompleteResponse {
	return proto.CompleteResponse{Error: &proto.Error{Message: fmt.Sprintf(format, args...)}}
}

func (h handler) Complete(ctx context.Context, req proto.CompleteRequest) proto.CompleteResponse {
	body, err := buildRequest(req)
	if err != nil {
		return fail("%v", err)
	}
	base := strings.TrimRight(h.getenv("OPENAI_BASE_URL"), "/")
	if base == "" {
		base = defaultBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fail("building request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := h.getenv("OPENAI_API_KEY"); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpResp, err := h.client.Do(httpReq)
	if err != nil {
		return fail("request failed: %v", err)
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fail("reading response (HTTP %d): %v", httpResp.StatusCode, err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		var e chatResponse
		if json.Unmarshal(data, &e) == nil && e.Error != nil && e.Error.Message != "" {
			resp := fail("HTTP %d: %s", httpResp.StatusCode, e.Error.Message)
			if e.Error.overflow() {
				resp.Error.Kind = proto.ErrContextOverflow
			}
			return resp
		}
		return fail("HTTP %d: %s", httpResp.StatusCode, clip(string(data), 2000))
	}
	return parseResponse(data)
}

func buildRequest(req proto.CompleteRequest) ([]byte, error) {
	var messages []any
	var system []string
	for _, s := range req.System {
		system = append(system, s.Text)
	}
	if len(system) > 0 {
		messages = append(messages, map[string]any{"role": "system", "content": strings.Join(system, "\n\n")})
	}
	for i, m := range req.Messages {
		switch m.Role {
		case "user":
			messages = append(messages, map[string]any{"role": "user", "content": m.Content})
		case "tool":
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": m.CallID, "content": m.Content})
		case "assistant":
			msg, err := assistantMessage(m)
			if err != nil {
				return nil, fmt.Errorf("message %d: %v", i, err)
			}
			messages = append(messages, msg)
		default:
			return nil, fmt.Errorf("message %d: unknown role %q", i, m.Role)
		}
	}

	tools := req.Tools
	if len(tools) == 0 {
		tools = []proto.Tool{proto.ShTool()}
	}
	var wireTools []any
	for _, t := range tools {
		wireTools = append(wireTools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			},
		})
	}

	body := map[string]any{
		"model":               req.Model,
		"messages":            messages,
		"tools":               wireTools,
		"parallel_tool_calls": false,
	}
	if v, ok := req.Options["max_tokens"]; ok {
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || n < 1 {
			return nil, fmt.Errorf("option max_tokens must be a positive integer, got %v", v)
		}
		body["max_tokens"] = int64(n)
	}
	if v, ok := req.Options["reasoning_effort"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("option reasoning_effort must be a string, got %v", v)
		}
		body["reasoning_effort"] = s
	}
	return json.Marshal(body)
}

func commandArguments(command string, wait json.RawMessage) string {
	v := map[string]any{"command": command}
	if len(wait) > 0 {
		v["wait"] = wait
	}
	args, _ := json.Marshal(v)
	return string(args)
}

// assistantMessage rebuilds an assistant turn. A stored provider message
// (opaque) is replayed with every field intact, so provider-specific fields
// such as reasoning_content survive. Only tool_calls is adjusted: the daemon
// assigns its own call_id and answers it in the next tool message, so the
// replayed call must carry that id, and only the call that was run is kept.
func assistantMessage(m proto.Message) (any, error) {
	if len(m.Opaque) == 0 {
		msg := map[string]any{"role": "assistant", "content": m.Text}
		if m.Command != nil {
			if m.Text == "" {
				msg["content"] = nil
			}
			msg["tool_calls"] = []any{map[string]any{
				"id":   m.CallID,
				"type": "function",
				"function": map[string]any{
					"name":      "sh",
					"arguments": commandArguments(*m.Command, m.Wait),
				},
			}}
		}
		return msg, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(m.Opaque, &fields); err != nil {
		return nil, fmt.Errorf("opaque is not a JSON object: %v", err)
	}
	if m.Command == nil {
		if _, ok := fields["tool_calls"]; !ok {
			return m.Opaque, nil
		}
		delete(fields, "tool_calls")
		return fields, nil
	}
	var calls []map[string]json.RawMessage
	if raw, ok := fields["tool_calls"]; ok {
		if err := json.Unmarshal(raw, &calls); err != nil {
			return nil, fmt.Errorf("opaque tool_calls: %v", err)
		}
	}
	if len(calls) == 0 {
		// Not a reply this plugin produced; describe the call from the message.
		calls = []map[string]json.RawMessage{{
			"type":     json.RawMessage(`"function"`),
			"function": mustJSON(map[string]string{"name": "sh", "arguments": commandArguments(*m.Command, m.Wait)}),
		}}
	}
	call := calls[0]
	call["id"] = mustJSON(m.CallID)
	fields["tool_calls"] = mustJSON([]any{call})
	return fields, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func parseResponse(data []byte) proto.CompleteResponse {
	var r chatResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return fail("invalid response: %v: %s", err, clip(string(data), 2000))
	}
	if r.Error != nil {
		return fail("API error: %s", r.Error.Message)
	}
	if len(r.Choices) == 0 || len(r.Choices[0].Message) == 0 {
		return fail("response has no choices: %s", clip(string(data), 2000))
	}
	raw := r.Choices[0].Message
	var msg responseMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fail("invalid response message: %v", err)
	}

	resp := proto.CompleteResponse{
		Text:   contentText(msg.Content),
		Opaque: raw,
		Usage: proto.Usage{
			InputTokens:     max(r.Usage.PromptTokens-r.Usage.PromptTokensDetails.CachedTokens, 0),
			OutputTokens:    r.Usage.CompletionTokens,
			CacheReadTokens: r.Usage.PromptTokensDetails.CachedTokens,
		},
	}
	if resp.Text == "" && msg.Refusal != "" {
		resp.Text = msg.Refusal
	}
	if len(msg.ToolCalls) > 0 {
		command, wait, err := parseCommand(msg.ToolCalls[0].Function.Arguments)
		if err != nil {
			return fail("tool call %s: %v", msg.ToolCalls[0].Function.Name, err)
		}
		resp.Command, resp.Wait = &command, wait
	}
	return resp
}

// parseCommand reads {"command": "...", "wait": ...} from a tool call's
// arguments, which are normally a JSON-encoded string but are an object on
// some servers. The wait is returned as given, or nil.
func parseCommand(raw json.RawMessage) (string, json.RawMessage, error) {
	args := []byte(raw)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		args = []byte(s)
	}
	var v struct {
		Command *string         `json:"command"`
		Wait    json.RawMessage `json:"wait"`
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return "", nil, fmt.Errorf("arguments are not valid JSON: %v: %s", err, clip(string(args), 500))
	}
	if v.Command == nil {
		return "", nil, fmt.Errorf("arguments have no command: %s", clip(string(args), 500))
	}
	return *v.Command, v.Wait, nil
}

// contentText accepts a string, null, or an array of text parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
