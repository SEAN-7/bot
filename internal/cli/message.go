package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/layout"
)

// MessageLimit bounds one message to the owner.
const MessageLimit = 64 << 10

// Message queues a message to the owner from the agent whose directory is
// home. The daemon records it in the owner conversation, which the agent
// cannot change afterwards.
func Message(home, text string, now time.Time) error {
	if home == "" {
		return errors.New("HOME is not set; run agent message from an agent's shell")
	}
	return queue(filepath.Join(home, ".messages"), text, now)
}

// cleanName reports whether name is one plain directory name.
func cleanName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

// SendChild queues a message from the agent whose directory is home to its
// direct child name. The daemon delivers it as the owner's, and records it in
// the child's conversation.
func SendChild(home, name, text string, now time.Time) error {
	if home == "" {
		return errors.New("HOME is not set; run agent send from an agent's shell")
	}
	if !cleanName(name) {
		return fmt.Errorf("%q is not a direct subagent name; use one name from ~/agents", name)
	}
	if info, err := os.Stat(filepath.Join(home, "agents", name, "config.json")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("no subagent %q (expected ~/agents/%s/config.json)", name, name)
	}
	return queue(filepath.Join(home, ".messages", "agents", name), text, now)
}

// queue writes one message into a spool directory with an atomic dot-name
// rename, so the daemon never reads half a message.
func queue(dir, text string, now time.Time) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("message is empty")
	}
	if len(text) > MessageLimit {
		return fmt.Errorf("message is over %d KiB; write the details to a file and send its path", MessageLimit>>10)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := now.UTC().Format("20060102T150405.000000000Z") + ".md"
	temp := filepath.Join(dir, "."+name)
	if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(dir, name))
}

// Target resolves whose journal and conversation to show. name is "" for the
// caller's own, or a subagent relative to the caller ("research" or
// "research/agents/scraper"). root is true for the human, whose own agent is
// the main agent. journalDir and conversation follow the agent's logical id
// (its parent's id plus "/agents/NAME"); errorFile is in the real control
// directory, which can sit under another agent's home, and is empty for a
// subagent asking about itself because its ERROR is in its parent's.
func Target(l layout.Layout, root bool, home, name string) (journalDir, errorFile, conversation string, err error) {
	var id, base string // the caller's agent id, and where its agents/ directory is
	switch {
	case root:
		id, base = l.Home, l.Home
	case home == "":
		return "", "", "", errors.New("HOME is not set; run this from an agent's shell")
	case home == l.Home:
		id, base = l.Home, home
	default:
		if id, err = idFromJournal(l, home); err != nil {
			return "", "", "", err
		}
		base = home
	}
	if name == "" {
		if id == l.Home {
			return l.JournalDir(l.Home), filepath.Join(l.Home, "ERROR"), l.MessagesFile(), nil
		}
		journalDir, err = filepath.EvalSymlinks(filepath.Join(home, "journal"))
		if err != nil {
			return "", "", "", err
		}
		return journalDir, "", filepath.Join(journalDir, "messages.jsonl"), nil
	}
	parts := strings.Split(name, "/agents/")
	for _, part := range parts {
		if !cleanName(part) {
			return "", "", "", fmt.Errorf("%q is not a subagent name; use NAME or NAME/agents/NAME", name)
		}
		id = filepath.Join(id, "agents", part)
	}
	ctrl, dir := "", base
	for i, part := range parts {
		ctrl = filepath.Join(dir, "agents", part)
		if i < len(parts)-1 {
			if dir, err = filepath.EvalSymlinks(filepath.Join(ctrl, "home")); err != nil {
				return "", "", "", fmt.Errorf("no subagent %q: %v", strings.Join(parts[:i+1], "/agents/"), err)
			}
		}
	}
	return l.JournalDir(id), filepath.Join(ctrl, "ERROR"), l.ConversationFile(id), nil
}

// idFromJournal finds the id of the agent living in home from where its
// journal link points: the journal directory mirrors the id under the log root.
func idFromJournal(l layout.Layout, home string) (string, error) {
	dir, err := filepath.EvalSymlinks(filepath.Join(home, "journal"))
	if err != nil {
		return "", fmt.Errorf("%s is not an agent's home (no ~/journal link): %v", home, err)
	}
	roots := []string{l.Log}
	if real, err := filepath.EvalSymlinks(l.Log); err == nil {
		roots = append(roots, real)
	}
	for _, root := range roots {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			return "/" + rel, nil
		}
	}
	return "", fmt.Errorf("%s: ~/journal points to %s, outside %s", home, dir, l.Log)
}

// Messages prints the conversation in path and follows new messages.
func Messages(ctx context.Context, path string, w io.Writer) error {
	var reader *bufio.Reader
	var partial []byte
	for {
		if reader == nil {
			if f, err := os.Open(path); err == nil {
				defer f.Close()
				reader = bufio.NewReader(f)
			}
		}
		for reader != nil {
			chunk, err := reader.ReadBytes('\n')
			partial = append(partial, chunk...)
			if err != nil {
				break
			}
			fmt.Fprintln(w, formatMessage(partial))
			partial = nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func formatMessage(line []byte) string {
	var m struct {
		T     string `json:"t"`
		From  string `json:"from"`
		Agent string `json:"agent"`
		Text  string `json:"text"`
	}
	if json.Unmarshal(line, &m) != nil {
		return sanitize(strings.TrimSpace(string(line)))
	}
	clock := m.T
	if t, err := time.Parse(time.RFC3339Nano, m.T); err == nil {
		clock = t.Local().Format("2006-01-02 15:04")
	}
	who := m.From
	if m.From == "agent" && m.Agent != "" {
		who = m.Agent // the main agent's conversation names the agent
	}
	text := strings.ReplaceAll(strings.TrimRight(m.Text, "\n"), "\n", "\n    ")
	return sanitize(fmt.Sprintf("%s %s: %s", clock, who, text))
}
