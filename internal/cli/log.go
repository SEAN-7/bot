package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/layout"
)

const resultLines = 20

// Format renders one journal record for people.
func Format(line []byte) string {
	var r map[string]any
	if err := json.Unmarshal(line, &r); err != nil {
		return string(line)
	}
	str := func(key string) string { s, _ := r[key].(string); return s }
	clock := str("t")
	if t, err := time.Parse(time.RFC3339Nano, clock); err == nil {
		clock = t.Local().Format("15:04:05")
	}
	indent := func(text string, limit int) string {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		extra := 0
		if limit > 0 && len(lines) > limit {
			extra, lines = len(lines)-limit, lines[:limit]
		}
		out := "  " + strings.Join(lines, "\n  ")
		if extra > 0 {
			out += fmt.Sprintf("\n  ... %d more lines", extra)
		}
		return out
	}
	switch typ := str("type"); typ {
	case "assistant":
		out := clock + " assistant: " + str("text")
		if cmd := str("command"); cmd != "" {
			out += "\n  $ " + cmd
		}
		return out
	case "result":
		code, _ := r["exit_code"].(float64)
		return fmt.Sprintf("%s result (exit %d):\n%s", clock, int(code), indent(str("output"), resultLines))
	case "inbox":
		return fmt.Sprintf("%s inbox %s [%s]\n%s", clock, str("file"), str("from"), indent(str("content"), resultLines))
	case "note":
		return clock + " note: " + str("text")
	case "end":
		return clock + " end (" + str("reason") + ")"
	case "start":
		return clock + " start " + str("agent")
	default:
		return clock + " " + typ
	}
}

// AgentDir resolves NAME ("" for the main agent, or a path under
// ~/agents such as "research/agents/scraper").
func AgentDir(l layout.Layout, name string) string {
	if name == "" {
		return l.Home
	}
	return filepath.Join(l.Home, "agents", filepath.Clean("/" + name)[1:])
}

func newest(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// Log prints the agent's ERROR, if any, then follows its newest journal,
// moving on to each new run as it starts.
func Log(ctx context.Context, l layout.Layout, name string, w io.Writer) error {
	dir := AgentDir(l, name)
	if data, err := os.ReadFile(filepath.Join(dir, "ERROR")); err == nil {
		fmt.Fprintf(w, "ERROR: %s\n", strings.TrimSpace(string(data)))
	}
	var file *os.File
	var reader *bufio.Reader
	var current string
	var partial []byte
	defer func() {
		if file != nil {
			file.Close()
		}
	}()
	for {
		for reader != nil {
			chunk, err := reader.ReadBytes('\n')
			partial = append(partial, chunk...)
			if err != nil {
				break // wait for the rest of the line
			}
			fmt.Fprintln(w, Format(partial))
			partial = nil
		}
		// Switch only after reading to EOF, so a finished run prints in full.
		if next := newest(l.JournalDir(dir)); next != "" && next != current {
			if file != nil {
				// Lines written after the last read but before the switch.
				rest, _ := io.ReadAll(reader)
				for _, line := range strings.Split(string(append(partial, rest...)), "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Fprintln(w, Format([]byte(line)))
					}
				}
				file.Close()
			}
			f, err := os.Open(next)
			if err != nil {
				return err
			}
			file, reader, current, partial = f, bufio.NewReader(f), next, nil
			fmt.Fprintf(w, "== %s\n", next)
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
