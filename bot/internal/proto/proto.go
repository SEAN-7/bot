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

// Image is a picture attached to a user message. Data is base64 in JSON.
type Image struct {
	File      string `json:"file"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// Message is one conversation entry. Roles are "user" (Content), "assistant"
// (Text, Command, Wait, CallID, Opaque) and "tool" (CallID, Content). Images
// is set on user messages only. Opaque is set by the plugin and returned to it
// unchanged.
type Message struct {
	Role    string          `json:"role"`
	Content string          `json:"content,omitempty"`
	Images  []Image         `json:"images,omitempty"`
	Text    string          `json:"text,omitempty"`
	Command *string         `json:"command,omitempty"`
	Wait    json.RawMessage `json:"wait,omitempty"`
	CallID  string          `json:"call_id,omitempty"`
	Opaque  json.RawMessage `json:"opaque,omitempty"`
}

type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Error reports a failed call. Kind lets the daemon tell failures that a
// retry cannot fix from transient ones.
type Error struct {
	Message string `json:"message"`
	Kind    string `json:"kind,omitempty"`
}

// ErrContextOverflow: the request exceeds the model's context window.
const ErrContextOverflow = "context_overflow"

type CompleteRequest struct {
	Protocol int            `json:"protocol"`
	Type     string         `json:"type"`
	Model    string         `json:"model"`
	Options  map[string]any `json:"options,omitempty"`
	System   []Segment      `json:"system"`
	Tools    []Tool         `json:"tools"`
	Messages []Message      `json:"messages"`
}

// CompleteResponse carries the model's reply. Wait is the sh call's wait
// argument exactly as the model gave it, absent when it gave none; the daemon
// interprets it.
type CompleteResponse struct {
	Protocol int             `json:"protocol"`
	Text     string          `json:"text"`
	Command  *string         `json:"command,omitempty"`
	Wait     json.RawMessage `json:"wait,omitempty"`
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

// Model is one model a plugin offers. ContextTokens, when known, is shown to
// agents choosing a model; the daemon does not use it. ImageTypes lists the
// image media types it accepts; none means it takes text only.
type Model struct {
	ID            string            `json:"id"`
	ContextTokens int               `json:"context_tokens,omitempty"`
	ImageTypes    []string          `json:"image_types,omitempty"`
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
		Description: "Run a shell command on this machine as your user, in your home directory. Returns combined output and exit status. A command still running after its wait is killed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The command to run"},"wait":{"type":"integer","description":"Seconds to wait for the command before killing it. Default 120, maximum 600."}},"required":["command"]}`),
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
		resp = CompleteResponse{Protocol: Version, Error: &Error{Message: "invalid request: " + err.Error()}}
	} else if req.Protocol != Version {
		resp = CompleteResponse{Protocol: Version, Error: &Error{Message: fmt.Sprintf("unsupported protocol %d (want %d)", req.Protocol, Version)}}
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
		resp = CompleteResponse{Protocol: Version, Error: &Error{Message: "unknown request type " + req.Type}}
	}
	return json.NewEncoder(w).Encode(resp)
}
