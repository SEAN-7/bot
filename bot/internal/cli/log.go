package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
)

const resultLines = 20

// sanitize drops control characters, so agent-written text cannot drive the
// operator's terminal with escape sequences.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// readError reads an agent's ERROR file safely when run as root: no
// symlinks, no FIFOs or devices, and at most 4 KiB.
func readError(path string) (string, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", false
	}
	return sanitize(strings.TrimSpace(string(data))), true
}

// Format renders one journal record for people.
func Format(line []byte) string {
	return sanitize(format(line))
}

func format(line []byte) string {
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

func newest(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		if journal.IsRun(filepath.Base(matches[i])) {
			return matches[i]
		}
	}
	return ""
}

// Log prints the agent's ERROR, if any, then its newest journal. An empty
// errorFile skips the ERROR.
func Log(journalDir, errorFile string, w io.Writer) error {
	if errorFile != "" {
		if text, ok := readError(errorFile); ok {
			fmt.Fprintf(w, "ERROR: %s\n", text)
		}
	}
	path := newest(journalDir)
	if path == "" {
		fmt.Fprintln(w, "No journal yet.")
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(w, "== %s\n", path)
	return printLines(f, w, Format)
}

// printLines writes each non-empty line of r as format renders it.
func printLines(r io.Reader, w io.Writer, format func([]byte) string) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			fmt.Fprintln(w, format(line))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
