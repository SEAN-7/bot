package proto

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageTotal(t *testing.T) {
	if got := (Usage{1, 2, 3, 4}).Total(); got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestShToolSchema(t *testing.T) {
	tool := ShTool()
	if tool.Name != "sh" {
		t.Fatalf("name %q", tool.Name)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "command" {
		t.Fatalf("required %v", schema.Required)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	cmd := "ls"
	in := Message{Role: "assistant", Command: &cmd, CallID: "c1", Opaque: json.RawMessage(`{"x":1}`)}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"call_id":"c1"`, `"opaque":{"x":1}`, `"command":"ls"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("missing %s in %s", key, data)
		}
	}
	var out Message
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Role != "assistant" || *out.Command != "ls" || out.CallID != "c1" || string(out.Opaque) != `{"x":1}` {
		t.Fatalf("got %+v", out)
	}
	none, _ := json.Marshal(Message{Role: "assistant"})
	if strings.Contains(string(none), "command") {
		t.Fatalf("nil command serialized: %s", none)
	}
}

type stub struct{}

func (stub) Describe() DescribeResponse {
	return DescribeResponse{Models: []Model{{ID: "m", ContextTokens: 10}}}
}
func (stub) Complete(ctx context.Context, r CompleteRequest) CompleteResponse {
	return CompleteResponse{Text: r.Model}
}

func TestServeRejectsUnknownProtocol(t *testing.T) {
	var out bytes.Buffer
	if err := ServeIO(stub{}, strings.NewReader(`{"protocol":2,"type":"describe"}`), &out); err != nil {
		t.Fatal(err)
	}
	var resp DescribeResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "protocol") {
		t.Fatalf("got %s", out.String())
	}
}

func TestServeDispatches(t *testing.T) {
	var out bytes.Buffer
	if err := ServeIO(stub{}, strings.NewReader(`{"protocol":1,"type":"complete","model":"m1"}`), &out); err != nil {
		t.Fatal(err)
	}
	var resp CompleteResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Protocol != 1 || resp.Text != "m1" {
		t.Fatalf("got %s", out.String())
	}
	out.Reset()
	ServeIO(stub{}, strings.NewReader(`{"protocol":1,"type":"describe"}`), &out)
	var d DescribeResponse
	json.Unmarshal(out.Bytes(), &d)
	if d.Protocol != 1 || d.Models[0].ContextTokens != 10 {
		t.Fatalf("got %s", out.String())
	}
}
