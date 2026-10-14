package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SEAN-7/bot/internal/proto"
)

const header = `{"models":[{"id":"replay-1","context_tokens":1000}]}`

func setup(t *testing.T, steps ...string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "script.jsonl")
	if err := os.WriteFile(script, []byte(strings.Join(append([]string{header}, steps...), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log.jsonl")
	t.Setenv("REPLAY_SCRIPT", script)
	t.Setenv("REPLAY_LOG", log)
	return log
}

func call(t *testing.T, req string) proto.CompleteResponse {
	t.Helper()
	var out bytes.Buffer
	if err := proto.ServeIO(handler{}, strings.NewReader(req), &out); err != nil {
		t.Fatal(err)
	}
	var resp proto.CompleteResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

const plain = `{"protocol":1,"type":"complete","model":"replay-1","messages":[{"role":"user","content":"hello"}]}`

func TestDescribeFromHeader(t *testing.T) {
	setup(t)
	var out bytes.Buffer
	proto.ServeIO(handler{}, strings.NewReader(`{"protocol":1,"type":"describe"}`), &out)
	var d proto.DescribeResponse
	json.Unmarshal(out.Bytes(), &d)
	if len(d.Models) != 1 || d.Models[0].ID != "replay-1" || d.Models[0].ContextTokens != 1000 {
		t.Fatalf("got %s", out.String())
	}
	if len(d.Settings) != 2 {
		t.Fatalf("settings %v", d.Settings)
	}
}

func TestStepsConsumedInOrder(t *testing.T) {
	log := setup(t, `{"command":"echo 1"}`, `{"command":"echo 2"}`)
	if r := call(t, plain); r.Command == nil || *r.Command != "echo 1" {
		t.Fatalf("first %+v", r)
	}
	if r := call(t, plain); r.Command == nil || *r.Command != "echo 2" {
		t.Fatalf("second %+v", r)
	}
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"index":0`) || !strings.Contains(lines[1], `"index":1`) {
		t.Fatalf("log %s", data)
	}
}

func TestMatchSelectsStep(t *testing.T) {
	setup(t, `{"match":"child-task","command":"child"}`, `{"command":"parent"}`)
	if r := call(t, plain); r.Command == nil || *r.Command != "parent" {
		t.Fatalf("got %+v", r)
	}
	child := strings.Replace(plain, "hello", "child-task: go", 1)
	if r := call(t, child); r.Command == nil || *r.Command != "child" {
		t.Fatalf("got %+v", r)
	}
}

func TestErrorStep(t *testing.T) {
	setup(t, `{"error":"boom"}`, `{"command":"after"}`)
	if r := call(t, plain); r.Error == nil || r.Error.Message != "boom" {
		t.Fatalf("got %+v", r)
	}
	if r := call(t, plain); r.Command == nil || *r.Command != "after" {
		t.Fatalf("error step not consumed: %+v", r)
	}
}

func TestExhaustedEndsRun(t *testing.T) {
	setup(t)
	r := call(t, plain)
	if r.Command != nil || r.Text != "" || r.Error != nil {
		t.Fatalf("got %+v", r)
	}
}

func TestUsageAndText(t *testing.T) {
	setup(t, `{"text":"hi","usage":{"input_tokens":5,"output_tokens":2}}`)
	r := call(t, plain)
	if r.Text != "hi" || r.Usage != (proto.Usage{InputTokens: 5, OutputTokens: 2}) {
		t.Fatalf("got %+v", r)
	}
}

func TestErrorKind(t *testing.T) {
	setup(t, `{"error":"too long","error_kind":"context_overflow"}`)
	if r := call(t, plain); r.Error == nil || r.Error.Kind != proto.ErrContextOverflow {
		t.Fatalf("got %+v", r.Error)
	}
}
