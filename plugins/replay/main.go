// Command replay is a scripted provider plugin for tests.
//
// REPLAY_SCRIPT is JSONL: a header {"models":[...]} followed by one step per
// line: {"match","text","command","usage","opaque","error"}. Each complete
// request consumes the first unconsumed step whose match is empty or appears
// in the request. REPLAY_LOG records {"index","request"} per call; a step is
// consumed once its index is in the log. With no step left, the reply has no
// command, which ends the run.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/SEAN-7/bot/internal/proto"
)

type step struct {
	Match   string          `json:"match"`
	Text    string          `json:"text"`
	Command *string         `json:"command"`
	Usage   proto.Usage     `json:"usage"`
	Opaque  json.RawMessage `json:"opaque"`
	Error   string          `json:"error"`
	Kind    string          `json:"error_kind"`
}

type handler struct{}

func main() { proto.Serve(handler{}) }

func load() ([]proto.Model, []step, error) {
	data, err := os.ReadFile(os.Getenv("REPLAY_SCRIPT"))
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var head struct {
		Models []proto.Model `json:"models"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		return nil, nil, fmt.Errorf("script header: %w", err)
	}
	var steps []step
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s step
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, nil, fmt.Errorf("script line %d: %w", i+2, err)
		}
		steps = append(steps, s)
	}
	return head.Models, steps, nil
}

func (handler) Describe() proto.DescribeResponse {
	settings := []proto.Setting{
		{Name: "REPLAY_SCRIPT", Description: "Path to the JSONL script"},
		{Name: "REPLAY_LOG", Description: "Path where requests are recorded"},
	}
	models, _, err := load()
	if err != nil {
		return proto.DescribeResponse{Settings: settings, Error: &proto.Error{Message: err.Error()}}
	}
	return proto.DescribeResponse{Settings: settings, Models: models}
}

func consumed() (map[int]bool, error) {
	seen := map[int]bool{}
	f, err := os.Open(os.Getenv("REPLAY_LOG"))
	if os.IsNotExist(err) {
		return seen, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 64<<20)
	for scanner.Scan() {
		var entry struct {
			Index int `json:"index"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			seen[entry.Index] = true
		}
	}
	return seen, scanner.Err()
}

func (handler) Complete(ctx context.Context, req proto.CompleteRequest) proto.CompleteResponse {
	fail := func(err error) proto.CompleteResponse {
		return proto.CompleteResponse{Error: &proto.Error{Message: err.Error()}}
	}
	_, steps, err := load()
	if err != nil {
		return fail(err)
	}
	// Parent and child agents may call concurrently; serialize step selection.
	lock, err := os.OpenFile(os.Getenv("REPLAY_LOG")+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fail(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fail(err)
	}
	seen, err := consumed()
	if err != nil {
		return fail(err)
	}
	raw, _ := json.Marshal(req)
	index := -1
	for i, s := range steps {
		if !seen[i] && (s.Match == "" || strings.Contains(string(raw), s.Match)) {
			index = i
			break
		}
	}
	line, _ := json.Marshal(map[string]any{"index": index, "request": req})
	f, err := os.OpenFile(os.Getenv("REPLAY_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fail(err)
	}
	_, err = f.Write(append(line, '\n'))
	f.Close()
	if err != nil {
		return fail(err)
	}
	if index < 0 {
		return proto.CompleteResponse{}
	}
	s := steps[index]
	if s.Error != "" {
		return proto.CompleteResponse{Error: &proto.Error{Message: s.Error, Kind: s.Kind}}
	}
	return proto.CompleteResponse{Text: s.Text, Command: s.Command, Usage: s.Usage, Opaque: s.Opaque}
}
