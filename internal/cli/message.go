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
	if strings.TrimSpace(text) == "" {
		return errors.New("message is empty")
	}
	if len(text) > MessageLimit {
		return fmt.Errorf("message is over %d KiB; write the details to a file and send its path", MessageLimit>>10)
	}
	dir := filepath.Join(home, ".messages")
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

// Messages prints the owner conversation and follows new messages.
func Messages(ctx context.Context, l layout.Layout, w io.Writer) error {
	var reader *bufio.Reader
	var partial []byte
	for {
		if reader == nil {
			if f, err := os.Open(l.MessagesFile()); err == nil {
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
	if m.From == "agent" {
		who = m.Agent
	}
	text := strings.ReplaceAll(strings.TrimRight(m.Text, "\n"), "\n", "\n    ")
	return sanitize(fmt.Sprintf("%s %s: %s", clock, who, text))
}
