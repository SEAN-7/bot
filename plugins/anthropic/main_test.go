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

// fakeAPI records each request and answers with the given status and body.
type fakeAPI struct {
	status   int
	body     string
	requests []recorded
}

type recorded struct {
	path   string
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fakeAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		f.requests = append(f.requests, recorded{r.URL.Path, r.Header.Clone(), body, raw})
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const okBody = `{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func newHandler(srv *httptest.Server) handler {
	return handler{apiKey: "test-key", baseURL: srv.URL}
}

func complete(t *testing.T, h handler, req proto.CompleteRequest) proto.CompleteResponse {
	t.Helper()
	if req.Model == "" {
		req.Model = "claude-opus-5-5"
	}
	if req.Tools == nil {
		req.Tools = []proto.Tool{proto.ShTool()}
	}
	return h.Complete(context.Background(), req)
}

func strp(s string) *string { return &s }

// blocks returns messages[i].content as a list of objects.
func blocks(t *testing.T, msg any) []map[string]any {
	t.Helper()
	m := msg.(map[string]any)
	list, ok := m["content"].([]any)
	if !ok {
		t.Fatalf("content is not a block list: %#v", m["content"])
	}
	var out []map[string]any
	for _, b := range list {
		out = append(out, b.(map[string]any))
	}
	return out
}

func countCacheControl(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "cache_control" {
				n++
			}
			n += countCacheControl(child)
		}
	case []any:
		for _, child := range x {
			n += countCacheControl(child)
		}
	}
	return n
}

func TestRequestShape(t *testing.T) {
	api := &fakeAPI{status: 200, body: okBody}
	srv := api.serve(t)
	resp := complete(t, newHandler(srv), proto.CompleteRequest{
		System:   []proto.Segment{{Text: "one"}, {Text: "two"}, {Text: "three"}},
		Messages: []proto.Message{{Role: "user", Content: "hello"}},
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	if len(api.requests) != 1 {
		t.Fatalf("requests: %d", len(api.requests))
	}
	r := api.requests[0]
	if r.path != "/v1/messages" {
		t.Fatalf("path %q", r.path)
	}
	if got := r.header.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key %q", got)
	}
	if got := r.header.Get("anthropic-version"); got != anthropicVersion {
		t.Fatalf("anthropic-version %q", got)
	}
	if r.body["model"] != "claude-opus-5-5" {
		t.Fatalf("model %v", r.body["model"])
	}

	system := r.body["system"].([]any)
	if len(system) != 3 {
		t.Fatalf("system blocks: %d", len(system))
	}
	for i, want := range []string{"one", "two", "three"} {
		b := system[i].(map[string]any)
		if b["type"] != "text" || b["text"] != want {
			t.Fatalf("system[%d] = %v", i, b)
		}
		_, cached := b["cache_control"]
		if cached != (i == 2) {
			t.Fatalf("system[%d] cache_control present=%v", i, cached)
		}
	}
	cc := system[2].(map[string]any)["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" {
		t.Fatalf("cache_control %v", cc)
	}

	msgs := r.body["messages"].([]any)
	last := blocks(t, msgs[len(msgs)-1])
	if _, ok := last[len(last)-1]["cache_control"]; !ok {
		t.Fatalf("last block of final message has no cache_control: %v", last)
	}
	if n := countCacheControl(r.body); n > maxCacheBreakpoints {
		t.Fatalf("%d cache breakpoints, limit %d", n, maxCacheBreakpoints)
	}

	tools := r.body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %v", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "sh" || tool["input_schema"] == nil || tool["description"] == "" {
		t.Fatalf("tool %v", tool)
	}
	choice := r.body["tool_choice"].(map[string]any)
	if choice["type"] != "auto" || choice["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice %v", choice)
	}
	if r.body["max_tokens"] != float64(64000) {
		t.Fatalf("default max_tokens %v", r.body["max_tokens"])
	}
	if _, ok := r.body["thinking"]; ok {
		t.Fatalf("thinking sent without option: %v", r.body["thinking"])
	}
	if _, ok := r.body["output_config"]; ok {
		t.Fatalf("output_config sent without option: %v", r.body["output_config"])
	}
}

func TestMessageMapping(t *testing.T) {
	api := &fakeAPI{status: 200, body: okBody}
	srv := api.serve(t)
	resp := complete(t, newHandler(srv), proto.CompleteRequest{
		System: []proto.Segment{{Text: "sys"}},
		Messages: []proto.Message{
			{Role: "user", Content: "first"},
			{Role: "user", Content: "second"},
			{Role: "assistant", Text: "running it", Command: strp("ls -la"), CallID: "c1"},
			{Role: "tool", CallID: "c1", Content: "total 0"},
			{Role: "user", Content: "after tool"},
			{Role: "assistant", Text: "all done"},
			{Role: "user", Content: "thanks"},
		},
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	msgs := api.requests[0].body["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("want 5 merged messages, got %d: %s", len(msgs), api.requests[0].raw)
	}
	roles := []string{"user", "assistant", "user", "assistant", "user"}
	for i, want := range roles {
		if got := msgs[i].(map[string]any)["role"]; got != want {
			t.Fatalf("messages[%d].role = %v, want %s", i, got, want)
		}
	}

	first := blocks(t, msgs[0])
	if len(first) != 2 || first[0]["text"] != "first" || first[1]["text"] != "second" {
		t.Fatalf("consecutive user messages not merged: %v", first)
	}

	asst := blocks(t, msgs[1])
	if len(asst) != 2 || asst[0]["type"] != "text" || asst[0]["text"] != "running it" {
		t.Fatalf("assistant blocks %v", asst)
	}
	use := asst[1]
	if use["type"] != "tool_use" || use["id"] != "c1" || use["name"] != "sh" {
		t.Fatalf("tool_use %v", use)
	}
	if input := use["input"].(map[string]any); input["command"] != "ls -la" || len(input) != 1 {
		t.Fatalf("tool_use input %v", use["input"])
	}

	res := blocks(t, msgs[2])
	if len(res) != 2 {
		t.Fatalf("tool result + user text should merge into one user message: %v", res)
	}
	if res[0]["type"] != "tool_result" || res[0]["tool_use_id"] != "c1" || res[0]["content"] != "total 0" {
		t.Fatalf("tool_result %v", res[0])
	}
	if res[1]["type"] != "text" || res[1]["text"] != "after tool" {
		t.Fatalf("user text after tool result %v", res[1])
	}

	plain := blocks(t, msgs[3])
	if len(plain) != 1 || plain[0]["type"] != "text" || plain[0]["text"] != "all done" {
		t.Fatalf("text-only assistant %v", plain)
	}
}

func TestOpaqueRoundTrip(t *testing.T) {
	content := `[{"type":"thinking","thinking":"","signature":"sig-abc=="},{"type":"text","text":"Listing."},{"type":"tool_use","id":"toolu_01X","name":"sh","input":{"command":"ls"}}]`
	api := &fakeAPI{status: 200, body: `{"type":"message","role":"assistant","content":` + content + `,"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":7}}`}
	srv := api.serve(t)
	h := newHandler(srv)

	first := complete(t, h, proto.CompleteRequest{
		Messages: []proto.Message{{Role: "user", Content: "list files"}},
	})
	if first.Error != nil {
		t.Fatalf("error: %s", first.Error.Message)
	}
	if !jsonEqual(t, first.Opaque, []byte(content)) {
		t.Fatalf("opaque %s, want %s", first.Opaque, content)
	}
	if first.Command == nil || *first.Command != "ls" {
		t.Fatalf("command %v", first.Command)
	}

	// The daemon assigns its own call_id and replays the opaque unchanged.
	second := complete(t, h, proto.CompleteRequest{
		Messages: []proto.Message{
			{Role: "user", Content: "list files"},
			{Role: "assistant", Text: first.Text, Command: first.Command, CallID: "call-1", Opaque: first.Opaque},
			{Role: "tool", CallID: "call-1", Content: "a\nb"},
		},
	})
	if second.Error != nil {
		t.Fatalf("error: %s", second.Error.Message)
	}
	msgs := api.requests[1].body["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("role %v", asst["role"])
	}
	replayed, _ := json.Marshal(asst["content"])
	if !jsonEqual(t, replayed, []byte(content)) {
		t.Fatalf("replayed %s, want %s", replayed, content)
	}
	// The tool result must answer the tool_use id inside the replayed blocks.
	res := blocks(t, msgs[2])
	if res[0]["type"] != "tool_result" || res[0]["tool_use_id"] != "toolu_01X" {
		t.Fatalf("tool_result %v", res[0])
	}
}

func TestResponseParsing(t *testing.T) {
	api := &fakeAPI{status: 200, body: `{"type":"message","role":"assistant","content":[
		{"type":"thinking","thinking":"hmm","signature":"s"},
		{"type":"text","text":"Checking."},
		{"type":"tool_use","id":"toolu_1","name":"sh","input":{"command":"ls"}}
	],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":22,"cache_read_input_tokens":33,"cache_creation_input_tokens":44}}`}
	srv := api.serve(t)
	resp := complete(t, newHandler(srv), proto.CompleteRequest{
		Messages: []proto.Message{{Role: "user", Content: "go"}},
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	if resp.Command == nil || *resp.Command != "ls" {
		t.Fatalf("command %v", resp.Command)
	}
	if resp.Text != "Checking." {
		t.Fatalf("text %q", resp.Text)
	}
	want := proto.Usage{InputTokens: 11, OutputTokens: 22, CacheReadTokens: 33, CacheWriteTokens: 44}
	if resp.Usage != want {
		t.Fatalf("usage %+v, want %+v", resp.Usage, want)
	}
}

func TestHTTPErrorBecomesError(t *testing.T) {
	api := &fakeAPI{status: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`}
	srv := api.serve(t)
	resp := complete(t, newHandler(srv), proto.CompleteRequest{
		Messages: []proto.Message{{Role: "user", Content: "go"}},
	})
	if resp.Error == nil {
		t.Fatalf("no error: %+v", resp)
	}
	for _, want := range []string{"429", "rate_limit_error", "exceeded your rate limit"} {
		if !strings.Contains(resp.Error.Message, want) {
			t.Fatalf("error %q lacks %q", resp.Error.Message, want)
		}
	}
	if resp.Command != nil {
		t.Fatalf("command on error: %v", *resp.Command)
	}
}

func TestOptionsPassThrough(t *testing.T) {
	api := &fakeAPI{status: 200, body: okBody}
	srv := api.serve(t)
	resp := complete(t, newHandler(srv), proto.CompleteRequest{
		Model:    "claude-sonnet-5-5",
		Options:  map[string]any{"max_tokens": float64(2048), "effort": "low", "thinking": "between_tools"},
		Messages: []proto.Message{{Role: "user", Content: "go"}},
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	body := api.requests[0].body
	if body["max_tokens"] != float64(2048) {
		t.Fatalf("max_tokens %v", body["max_tokens"])
	}
	if oc := body["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Fatalf("output_config %v", oc)
	}
	if th := body["thinking"].(map[string]any); th["type"] != "between_tools" || len(th) != 1 {
		t.Fatalf("thinking %v", th)
	}

	bad := complete(t, newHandler(srv), proto.CompleteRequest{
		Options:  map[string]any{"temperature": 0.5},
		Messages: []proto.Message{{Role: "user", Content: "go"}},
	})
	if bad.Error == nil || !strings.Contains(bad.Error.Message, "temperature") {
		t.Fatalf("unknown option not rejected: %+v", bad)
	}
}

func TestDescribeOffline(t *testing.T) {
	d := handler{}.Describe()
	if d.Error != nil {
		t.Fatalf("error: %s", d.Error.Message)
	}
	settings := map[string]bool{}
	for _, s := range d.Settings {
		settings[s.Name] = s.Secret
	}
	if secret, ok := settings["ANTHROPIC_API_KEY"]; !ok || !secret {
		t.Fatalf("ANTHROPIC_API_KEY setting %v", d.Settings)
	}
	if secret, ok := settings["ANTHROPIC_BASE_URL"]; !ok || secret {
		t.Fatalf("ANTHROPIC_BASE_URL setting %v", d.Settings)
	}
	if len(d.Models) == 0 {
		t.Fatal("no models")
	}
	seen := map[string]bool{}
	for _, m := range d.Models {
		if seen[m.ID] {
			t.Fatalf("duplicate model %s", m.ID)
		}
		seen[m.ID] = true
		if m.ContextTokens <= 0 {
			t.Fatalf("%s context %d", m.ID, m.ContextTokens)
		}
		mt, ok := m.Options["max_tokens"]
		if !ok || mt.Type != "integer" || mt.Min == nil || mt.Max == nil || *mt.Min < 1 || *mt.Max < *mt.Min {
			t.Fatalf("%s max_tokens option %+v", m.ID, mt)
		}
		if e, ok := m.Options["effort"]; !ok || len(e.Values) == 0 {
			t.Fatalf("%s effort option %+v", m.ID, e)
		}
	}
	if !seen["claude-opus-5-5"] {
		t.Fatal("current default model claude-opus-5-5 missing")
	}

	// describe also works through the wire protocol without credentials.
	var out bytes.Buffer
	if err := proto.ServeIO(handler{}, strings.NewReader(`{"protocol":1,"type":"describe"}`), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"claude-opus-5-5"`) {
		t.Fatalf("describe output %s", out.String())
	}
}

func TestMissingAPIKey(t *testing.T) {
	resp := handler{}.Complete(context.Background(), proto.CompleteRequest{
		Model:    "claude-opus-5-5",
		Messages: []proto.Message{{Role: "user", Content: "go"}},
	})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "ANTHROPIC_API_KEY") {
		t.Fatalf("missing key not reported: %+v", resp)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("bad JSON %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("bad JSON %s: %v", b, err)
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}
