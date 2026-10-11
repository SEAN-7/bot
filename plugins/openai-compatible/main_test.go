package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SEAN-7/bot/internal/proto"
)

// fakeServer records each request and answers with the given status and body.
type fakeServer struct {
	*httptest.Server
	status int
	body   string
	paths  []string
	auth   []string
	bodies []map[string]any
}

func newFake(t *testing.T, status int, body string) *fakeServer {
	t.Helper()
	f := &fakeServer{status: status, body: body}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Errorf("request body is not JSON: %v: %s", err, data)
		}
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.bodies = append(f.bodies, m)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
	}))
	t.Cleanup(f.Close)
	return f
}

func testHandler(env map[string]string) handler {
	return handler{getenv: func(k string) string { return env[k] }, client: http.DefaultClient}
}

func handlerFor(f *fakeServer) handler {
	return testHandler(map[string]string{
		"OPENAI_BASE_URL": f.URL + "/v1/",
		"OPENAI_API_KEY":  "sk-test",
		"OPENAI_MODELS":   "m1=8000",
	})
}

func strp(s string) *string { return &s }

const toolCallResponse = `{
  "id": "chatcmpl-1",
  "object": "chat.completion",
  "choices": [{
    "index": 0,
    "finish_reason": "tool_calls",
    "message": {
      "role": "assistant",
      "content": "Listing files.",
      "tool_calls": [{"id": "call_abc", "type": "function", "function": {"name": "sh", "arguments": "{\"command\":\"ls -la\"}"}}]
    }
  }],
  "usage": {"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110, "prompt_tokens_details": {"cached_tokens": 40}}
}`

const textResponse = `{
  "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
}`

func baseRequest() proto.CompleteRequest {
	return proto.CompleteRequest{
		Protocol: proto.Version,
		Type:     "complete",
		Model:    "m1",
		System:   []proto.Segment{{Text: "one"}, {Text: "two"}, {Text: "three"}},
		Tools:    []proto.Tool{proto.ShTool()},
		Messages: []proto.Message{{Role: "user", Content: "hello"}},
	}
}

func messagesOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing: %v", body)
	}
	var out []map[string]any
	for _, m := range raw {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestDescribeFromSetting(t *testing.T) {
	d := testHandler(map[string]string{"OPENAI_MODELS": "a=8000,b=128000"}).Describe()
	if d.Error != nil {
		t.Fatalf("unexpected error: %v", d.Error.Message)
	}
	if len(d.Models) != 2 || d.Models[0].ID != "a" || d.Models[0].ContextTokens != 8000 ||
		d.Models[1].ID != "b" || d.Models[1].ContextTokens != 128000 {
		t.Fatalf("models = %+v", d.Models)
	}
	for _, m := range d.Models {
		mt, ok := m.Options["max_tokens"]
		if !ok || mt.Type != "integer" || mt.Min == nil || *mt.Min != 1 || mt.Max == nil || *mt.Max != 1000000 {
			t.Errorf("%s max_tokens option = %+v", m.ID, mt)
		}
		re, ok := m.Options["reasoning_effort"]
		if !ok || len(re.Values) == 0 {
			t.Errorf("%s reasoning_effort option = %+v", m.ID, re)
		}
		for _, want := range []string{"low", "medium", "high"} {
			found := false
			for _, v := range re.Values {
				if v == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s reasoning_effort lacks %q: %v", m.ID, want, re.Values)
			}
		}
	}
	names := map[string]bool{}
	for _, s := range d.Settings {
		names[s.Name] = true
		if s.Name == "OPENAI_API_KEY" && !s.Secret {
			t.Error("OPENAI_API_KEY must be secret")
		}
	}
	for _, n := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODELS"} {
		if !names[n] {
			t.Errorf("setting %s missing", n)
		}
	}

	// Without OPENAI_MODELS the installer must still learn the settings.
	empty := testHandler(map[string]string{}).Describe()
	if empty.Error != nil || len(empty.Models) != 0 || len(empty.Settings) != 3 {
		t.Fatalf("describe without models = %+v", empty)
	}
}

func TestBadModelsSetting(t *testing.T) {
	for _, s := range []string{"a=notanumber", "a", "=100", "a=0", "a=-5", "a=10,a=20"} {
		d := testHandler(map[string]string{"OPENAI_MODELS": s}).Describe()
		if d.Error == nil {
			t.Errorf("OPENAI_MODELS %q: expected an error, got %+v", s, d.Models)
		}
		if len(d.Settings) != 3 {
			t.Errorf("OPENAI_MODELS %q: settings should still be listed", s)
		}
	}
}

func TestRequestShape(t *testing.T) {
	f := newFake(t, 200, textResponse)
	resp := handlerFor(f).Complete(context.Background(), baseRequest())
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
	if len(f.bodies) != 1 {
		t.Fatalf("requests = %d", len(f.bodies))
	}
	if f.paths[0] != "POST /v1/chat/completions" {
		t.Errorf("path = %s", f.paths[0])
	}
	if f.auth[0] != "Bearer sk-test" {
		t.Errorf("Authorization = %q", f.auth[0])
	}
	body := f.bodies[0]
	if body["model"] != "m1" {
		t.Errorf("model = %v", body["model"])
	}
	if body["parallel_tool_calls"] != false {
		t.Errorf("parallel_tool_calls = %v", body["parallel_tool_calls"])
	}
	for _, k := range []string{"max_tokens", "max_completion_tokens", "reasoning_effort", "stream"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s sent without being set", k)
		}
	}
	msgs := messagesOf(t, body)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "one\n\ntwo\n\nthree" {
		t.Errorf("system message = %v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "hello" {
		t.Errorf("user message = %v", msgs[1])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	if tool["type"] != "function" || fn["name"] != "sh" || fn["description"] != proto.ShTool().Description {
		t.Errorf("tool = %v", tool)
	}
	params, _ := fn["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["command"] == nil {
		t.Errorf("tool parameters = %v", fn["parameters"])
	}
	if resp.Text != "done" || resp.Command != nil {
		t.Errorf("response = %+v", resp)
	}

	// No API key (local servers): no Authorization header.
	h := handlerFor(f)
	h.getenv = func(k string) string {
		if k == "OPENAI_BASE_URL" {
			return f.URL + "/v1"
		}
		return ""
	}
	if r := h.Complete(context.Background(), baseRequest()); r.Error != nil {
		t.Fatalf("unexpected error: %s", r.Error.Message)
	}
	if f.auth[1] != "" || f.paths[1] != "POST /v1/chat/completions" {
		t.Errorf("without key: auth %q path %s", f.auth[1], f.paths[1])
	}
}

func TestToolCallMapping(t *testing.T) {
	f := newFake(t, 200, toolCallResponse)
	req := baseRequest()
	req.Messages = append(req.Messages,
		proto.Message{Role: "assistant", Text: "Checking.", Command: strp("pwd"), CallID: "c1"},
		proto.Message{Role: "tool", CallID: "c1", Content: "/home/agent\n[exit 0]"},
		proto.Message{Role: "assistant", Command: strp(`echo "a\b"`), CallID: "c2"},
		proto.Message{Role: "tool", CallID: "c2", Content: "a\\b"},
	)
	resp := handlerFor(f).Complete(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
	if resp.Command == nil || *resp.Command != "ls -la" {
		t.Fatalf("command = %v", resp.Command)
	}
	if resp.Text != "Listing files." {
		t.Errorf("text = %q", resp.Text)
	}

	msgs := messagesOf(t, f.bodies[0])
	if len(msgs) != 6 {
		t.Fatalf("messages = %v", msgs)
	}
	check := func(m map[string]any, text any, id, command string) {
		t.Helper()
		if m["role"] != "assistant" || m["content"] != text {
			t.Errorf("assistant = %v", m)
		}
		calls, _ := m["tool_calls"].([]any)
		if len(calls) != 1 {
			t.Fatalf("tool_calls = %v", m["tool_calls"])
		}
		call := calls[0].(map[string]any)
		fn := call["function"].(map[string]any)
		if call["id"] != id || call["type"] != "function" || fn["name"] != "sh" {
			t.Errorf("tool call = %v", call)
		}
		argStr, ok := fn["arguments"].(string)
		if !ok {
			t.Fatalf("arguments must be a JSON string, got %T", fn["arguments"])
		}
		var args map[string]string
		if err := json.Unmarshal([]byte(argStr), &args); err != nil || args["command"] != command {
			t.Errorf("arguments = %q (%v)", argStr, err)
		}
	}
	check(msgs[2], "Checking.", "c1", "pwd")
	check(msgs[4], nil, "c2", `echo "a\b"`)
	if msgs[3]["role"] != "tool" || msgs[3]["tool_call_id"] != "c1" || msgs[3]["content"] != "/home/agent\n[exit 0]" {
		t.Errorf("tool message = %v", msgs[3])
	}
	if msgs[5]["tool_call_id"] != "c2" || msgs[5]["content"] != "a\\b" {
		t.Errorf("tool message = %v", msgs[5])
	}

	// Some servers return arguments as an object rather than a string.
	g := newFake(t, 200, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"sh","arguments":{"command":"date"}}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	r := handlerFor(g).Complete(context.Background(), baseRequest())
	if r.Error != nil || r.Command == nil || *r.Command != "date" || r.Text != "" {
		t.Errorf("object arguments: %+v %v", r, r.Error)
	}

	// Unparseable arguments are an error, not an empty command.
	b := newFake(t, 200, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"sh","arguments":"{not json"}}]}}]}`)
	if r := handlerFor(b).Complete(context.Background(), baseRequest()); r.Error == nil || r.Command != nil {
		t.Errorf("bad arguments: %+v", r)
	}
}

func TestOpaqueReplay(t *testing.T) {
	const reply = `{"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"Let me think about it.","provider_extra":{"sig":"abc=="},"tool_calls":[{"id":"call_provider","type":"function","function":{"name":"sh","arguments":"{\"command\":\"uptime\"}"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	f := newFake(t, 200, reply)
	h := handlerFor(f)
	first := h.Complete(context.Background(), baseRequest())
	if first.Error != nil {
		t.Fatalf("unexpected error: %s", first.Error.Message)
	}
	if first.Command == nil || *first.Command != "uptime" {
		t.Fatalf("command = %v", first.Command)
	}
	var opaque map[string]any
	if err := json.Unmarshal(first.Opaque, &opaque); err != nil {
		t.Fatalf("opaque is not a JSON object: %v: %s", err, first.Opaque)
	}
	if opaque["reasoning_content"] != "Let me think about it." {
		t.Fatalf("opaque = %s", first.Opaque)
	}

	// The daemon stores the reply with its own call_id and sends it back.
	req := baseRequest()
	req.Messages = append(req.Messages,
		proto.Message{Role: "assistant", Text: first.Text, Command: first.Command, CallID: "call-1", Opaque: first.Opaque},
		proto.Message{Role: "tool", CallID: "call-1", Content: "up 3 days"},
	)
	if r := h.Complete(context.Background(), req); r.Error != nil {
		t.Fatalf("unexpected error: %s", r.Error.Message)
	}
	msgs := messagesOf(t, f.bodies[1])
	if len(msgs) != 4 {
		t.Fatalf("messages = %v", msgs)
	}
	got := msgs[2]
	// Every provider field is replayed unchanged...
	want := map[string]any{}
	json.Unmarshal(first.Opaque, &want)
	for k, v := range want {
		if k == "tool_calls" {
			continue
		}
		gv, _ := json.Marshal(got[k])
		wv, _ := json.Marshal(v)
		if !bytes.Equal(gv, wv) {
			t.Errorf("field %s = %s, want %s", k, gv, wv)
		}
	}
	if len(got) != len(want) {
		t.Errorf("replayed message has fields %v, want %v", got, want)
	}
	// ...except the tool call id, which must pair with the daemon's tool message.
	calls := got["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %v", calls)
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call-1" || call["function"].(map[string]any)["arguments"] != `{"command":"uptime"}` {
		t.Errorf("replayed tool call = %v", call)
	}
	if msgs[3]["tool_call_id"] != "call-1" {
		t.Errorf("tool message = %v", msgs[3])
	}

	// A text-only reply's opaque is replayed exactly.
	g := newFake(t, 200, `{"choices":[{"message":{"role":"assistant","content":"hi","reasoning_content":"r"}}]}`)
	hg := handlerFor(g)
	r1 := hg.Complete(context.Background(), baseRequest())
	req2 := baseRequest()
	req2.Messages = append(req2.Messages, proto.Message{Role: "assistant", Text: r1.Text, Opaque: r1.Opaque}, proto.Message{Role: "user", Content: "again"})
	hg.Complete(context.Background(), req2)
	m := messagesOf(t, g.bodies[1])[2]
	if m["reasoning_content"] != "r" || m["content"] != "hi" || len(m) != 3 {
		t.Errorf("replayed text message = %v", m)
	}
}

func TestUsageMapping(t *testing.T) {
	f := newFake(t, 200, toolCallResponse)
	resp := handlerFor(f).Complete(context.Background(), baseRequest())
	want := proto.Usage{InputTokens: 60, OutputTokens: 10, CacheReadTokens: 40, CacheWriteTokens: 0}
	if resp.Usage != want {
		t.Fatalf("usage = %+v, want %+v", resp.Usage, want)
	}
	g := newFake(t, 200, textResponse) // no prompt_tokens_details
	if u := handlerFor(g).Complete(context.Background(), baseRequest()).Usage; u != (proto.Usage{InputTokens: 5, OutputTokens: 1}) {
		t.Fatalf("usage without details = %+v", u)
	}
}

func TestHTTPErrorBecomesError(t *testing.T) {
	f := newFake(t, 429, `{"error":{"message":"Rate limit reached","type":"requests"}}`)
	resp := handlerFor(f).Complete(context.Background(), baseRequest())
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "429") || !strings.Contains(resp.Error.Message, "Rate limit reached") {
		t.Fatalf("error = %+v", resp.Error)
	}
	if resp.Command != nil {
		t.Errorf("command set on error")
	}

	// An error object in a 200 response is still an error.
	g := newFake(t, 200, `{"error":{"message":"upstream failed"}}`)
	if r := handlerFor(g).Complete(context.Background(), baseRequest()); r.Error == nil || !strings.Contains(r.Error.Message, "upstream failed") {
		t.Fatalf("200 error = %+v", r.Error)
	}

	// No choices is an error.
	e := newFake(t, 200, `{"choices":[]}`)
	if r := handlerFor(e).Complete(context.Background(), baseRequest()); r.Error == nil {
		t.Fatal("expected error for empty choices")
	}

	// Unreachable server.
	h := testHandler(map[string]string{"OPENAI_BASE_URL": "http://127.0.0.1:1"})
	if r := h.Complete(context.Background(), baseRequest()); r.Error == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestOptionsPassThrough(t *testing.T) {
	f := newFake(t, 200, textResponse)
	req := baseRequest()
	// Options arrive from JSON, so numbers are float64.
	req.Options = map[string]any{"max_tokens": float64(4096), "reasoning_effort": "high"}
	if r := handlerFor(f).Complete(context.Background(), req); r.Error != nil {
		t.Fatalf("unexpected error: %s", r.Error.Message)
	}
	body := f.bodies[0]
	if body["max_tokens"] != float64(4096) || body["reasoning_effort"] != "high" {
		t.Errorf("options in body: max_tokens=%v reasoning_effort=%v", body["max_tokens"], body["reasoning_effort"])
	}

	for _, bad := range []map[string]any{
		{"max_tokens": "lots"},
		{"max_tokens": 1.5},
		{"reasoning_effort": 3.0},
	} {
		req.Options = bad
		if r := handlerFor(f).Complete(context.Background(), req); r.Error == nil {
			t.Errorf("options %v: expected error", bad)
		}
	}
}

func TestServeDescribe(t *testing.T) {
	var out bytes.Buffer
	if err := proto.ServeIO(testHandler(map[string]string{"OPENAI_MODELS": "a=8000"}), strings.NewReader(`{"protocol":1,"type":"describe"}`), &out); err != nil {
		t.Fatal(err)
	}
	var d proto.DescribeResponse
	if err := json.Unmarshal(out.Bytes(), &d); err != nil || d.Protocol != 1 || len(d.Models) != 1 {
		t.Fatalf("describe over protocol = %s (%v)", out.Bytes(), err)
	}
}

func TestContextOverflowKind(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		`{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"BadRequestError"}}`,
	} {
		f := newFake(t, 400, body)
		if r := handlerFor(f).Complete(context.Background(), baseRequest()); r.Error == nil || r.Error.Kind != proto.ErrContextOverflow {
			t.Fatalf("%s: got %+v", body, r.Error)
		}
	}
	f := newFake(t, 400, `{"error":{"message":"invalid model"}}`)
	if r := handlerFor(f).Complete(context.Background(), baseRequest()); r.Error == nil || r.Error.Kind != "" {
		t.Fatalf("other 400 marked as overflow: %+v", r.Error)
	}
}
