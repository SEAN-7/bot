// Package proto defines version 1 of the provider plugin protocol: one JSON
// request on the plugin's stdin, one JSON response on its stdout.
package proto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const Version = 1

type Segment struct {
	Text string `json:"text"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Message is one conversation entry. Roles are "user" (Content), "assistant"
// (Text, Command, CallID, Opaque) and "tool" (CallID, Content). Opaque is set
// by the plugin and returned to it unchanged.
type Message struct {
	Role    string          `json:"role"`
	Content string          `json:"content,omitempty"`
	Text    string          `json:"text,omitempty"`
	Command *string         `json:"command,omitempty"`
	CallID  string          `json:"call_id,omitempty"`
	Opaque  json.RawMessage `json:"opaque,omitempty"`
}

type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Total is the context consumed by a call: everything sent plus the reply.
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

type Error struct {
	Message string `json:"message"`
}

type CompleteRequest struct {
	Protocol int            `json:"protocol"`
	Type     string         `json:"type"`
	Model    string         `json:"model"`
	Options  map[string]any `json:"options,omitempty"`
	System   []Segment      `json:"system"`
	Tools    []Tool         `json:"tools"`
	Messages []Message      `json:"messages"`
}

type CompleteResponse struct {
	Protocol int             `json:"protocol"`
	Text     string          `json:"text"`
	Command  *string         `json:"command,omitempty"`
	Usage    Usage           `json:"usage"`
	Opaque   json.RawMessage `json:"opaque,omitempty"`
	Error    *Error          `json:"error,omitempty"`
}

type Setting struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Secret      bool   `json:"secret"`
}

// Option describes one accepted option: either an enumeration (Values) or an
// integer range (Type "integer", Min, Max).
type Option struct {
	Description string `json:"description"`
	Values      []any  `json:"values,omitempty"`
	Type        string `json:"type,omitempty"`
	Min         *int   `json:"min,omitempty"`
	Max         *int   `json:"max,omitempty"`
}

type Model struct {
	ID            string            `json:"id"`
	ContextTokens int               `json:"context_tokens"`
	Options       map[string]Option `json:"options,omitempty"`
}

type DescribeResponse struct {
	Protocol int       `json:"protocol"`
	Settings []Setting `json:"settings"`
	Models   []Model   `json:"models"`
	Error    *Error    `json:"error,omitempty"`
}

// ShTool is the single tool every model receives.
func ShTool() Tool {
	return Tool{
		Name:        "sh",
		Description: "Run a shell command on this machine as your user, in your home directory. Returns combined output and exit status.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The command to run"}},"required":["command"]}`),
	}
}

// Handler is implemented by plugins.
type Handler interface {
	Describe() DescribeResponse
	Complete(context.Context, CompleteRequest) CompleteResponse
}

// Serve handles one request from stdin and exits.
func Serve(h Handler) {
	if err := ServeIO(h, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func ServeIO(h Handler, r io.Reader, w io.Writer) error {
	var req struct {
		Protocol int    `json:"protocol"`
		Type     string `json:"type"`
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	var resp any
	if err := json.Unmarshal(data, &req); err != nil {
		resp = CompleteResponse{Protocol: Version, Error: &Error{"invalid request: " + err.Error()}}
	} else if req.Protocol != Version {
		resp = CompleteResponse{Protocol: Version, Error: &Error{fmt.Sprintf("unsupported protocol %d (want %d)", req.Protocol, Version)}}
	} else if req.Type == "describe" {
		d := h.Describe()
		d.Protocol = Version
		resp = d
	} else if req.Type == "complete" {
		var full CompleteRequest
		if err := json.Unmarshal(data, &full); err != nil {
			return err
		}
		c := h.Complete(context.Background(), full)
		c.Protocol = Version
		resp = c
	} else {
		resp = CompleteResponse{Protocol: Version, Error: &Error{"unknown request type " + req.Type}}
	}
	return json.NewEncoder(w).Encode(resp)
}
