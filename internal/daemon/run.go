package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/internal/shell"
)

// segmentLimit bounds README.md and TASK.md as read into the system prompt.
const segmentLimit = 1 << 20

type recorder interface {
	Record(typ string, fields map[string]any) error
}

func (a *agent) logf(format string, args ...any) {
	a.d.cfg.Logger.Printf("%s: %s", a.d.rel(a.dir), fmt.Sprintf(format, args...))
}

// errConfig marks a problem the agent must fix in config.json, as opposed
// to a provider failure, which is retried.
type errConfig struct{ error }

// gate reads config.json and validates it against the provider's describe.
// A config error is reported once per distinct config.json content and the
// agent waits until the file changes; provider failures are retried.
func (a *agent) gate(ctx context.Context) (plugin.Config, proto.DescribeResponse, bool) {
	d := a.d
	var describe proto.DescribeResponse
	data, _, err := d.fs.Read(filepath.Join(a.dir, "config.json"), 1<<20)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if err != nil {
		hash = "unreadable: " + err.Error()
	}
	if hash == a.failedConfig {
		return plugin.Config{}, describe, false
	}
	var cfg plugin.Config
	if err == nil {
		cfg, err = plugin.ParseConfig(data)
	}
	if err == nil {
		err = a.retry(ctx, func() error {
			installed, err := d.runner.Installed()
			if err != nil {
				return err
			}
			if !slices.Contains(installed, cfg.Provider) {
				return errConfig{plugin.Validate(cfg, installed, describe)}
			}
			if describe, err = d.runner.Describe(ctx, cfg.Provider); err != nil {
				return err
			}
			if err := plugin.Validate(cfg, installed, describe); err != nil {
				return errConfig{err}
			}
			return nil
		})
		var bad errConfig
		if errors.As(err, &bad) {
			err = bad.error
		} else if err != nil {
			return cfg, describe, false // stopped while retrying
		}
	}
	if err != nil {
		a.failedConfig = hash
		text := MsgConfigPrefix + err.Error()
		a.logf("not starting: %s", text)
		a.setError(text)
		if parent := d.parent(a.dir); parent != "" {
			if err := d.writeNote(parent, "subagent-error", fmt.Sprintf(MsgSubagentFmt, d.rel(a.dir), text)); err != nil {
				a.logf("notify parent: %v", err)
			}
		}
		return cfg, describe, false
	}
	if a.failedConfig != "" {
		a.failedConfig = ""
		a.clearError(MsgConfigPrefix)
	}
	return cfg, describe, true
}

func (a *agent) run(ctx context.Context) {
	cfg, describe, ok := a.gate(ctx)
	if !ok {
		return
	}
	jdir := a.d.cfg.JournalDir(a.dir)
	if !strings.HasPrefix(jdir, filepath.Clean(a.d.cfg.Log)+"/") {
		a.logf("journal path %q escapes %s", jdir, a.d.cfg.Log)
		return
	}
	j, err := journal.Create(jdir, time.Now())
	if err != nil {
		a.logf("journal: %v", err)
		return
	}
	if reason := a.steps(ctx, j, cfg, plugin.ContextTokens(describe, cfg.Model)); reason != "" {
		j.End(reason)
	} else {
		j.Close() // daemon shutdown: left unclean so recovery resumes the agent
	}
}

// stopReason distinguishes removal of the agent from daemon shutdown.
func (a *agent) stopReason(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), errRemoved) {
		return "removed"
	}
	return ""
}

func (a *agent) readSegment(name string) string {
	data, _, err := a.d.fs.Read(filepath.Join(a.dir, name), segmentLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	} else if err != nil {
		return fmt.Sprintf("(%s could not be read: %v)", name, err)
	}
	return strings.ToValidUTF8(string(data), "�")
}

func (a *agent) steps(ctx context.Context, j *journal.Writer, cfg plugin.Config, contextTokens int) string {
	d := a.d
	j.Record("start", map[string]any{"agent": a.dir, "config": cfg, "context_tokens": contextTokens})
	if err := d.fs.Symlink(d.cfg.JournalDir(a.dir), filepath.Join(a.dir, "journal")); err != nil {
		a.logf("journal link: %v", err)
	}
	system := []proto.Segment{{Text: SystemPrompt}, {Text: a.readSegment("README.md")}, {Text: a.readSegment("TASK.md")}}
	records, err := a.collectInbox(j)
	if err != nil {
		a.logf("inbox: %v", err)
		j.Record("note", map[string]any{"text": "inbox: " + err.Error()})
		return "error"
	}
	messages := []proto.Message{{Role: "user", Content: encodeRecords(records)}}
	runID := strings.TrimSuffix(filepath.Base(j.Path()), ".jsonl")
	warned, warnNext := false, false
	for n := 1; ; n++ {
		resp, err := a.complete(ctx, cfg, system, messages)
		if err != nil {
			return a.stopReason(ctx)
		}
		callID := fmt.Sprintf("call-%d", n)
		msg := proto.Message{Role: "assistant", Text: resp.Text, CallID: callID, Opaque: resp.Opaque}
		if resp.Command != nil && strings.TrimSpace(*resp.Command) != "" {
			msg.Command = resp.Command
		}
		messages = append(messages, msg)
		j.Record("assistant", map[string]any{"text": msg.Text, "command": msg.Command, "call_id": callID, "usage": resp.Usage, "opaque": msg.Opaque})
		if used := resp.Usage.Total(); contextTokens > 0 && msg.Command != nil {
			switch {
			case used*100 >= 95*contextTokens && n == 1:
				// A fresh run is already too big: a reset note would loop forever.
				text := fmt.Sprintf(MsgGuardFmt, used, contextTokens)
				a.logf("%s", text)
				a.setError(text)
				return "context-guard"
			case used*100 >= 95*contextTokens:
				if err := d.writeNote(a.dir, "reset", fmt.Sprintf(MsgResetFmt, j.Path())); err != nil {
					a.logf("reset note: %v", err)
				}
				return "context"
			case used*100 >= 80*contextTokens && !warned:
				warned, warnNext = true, true
			}
		}
		if n == 1 {
			a.clearError(errGuardPrefix)
		}
		if msg.Command == nil {
			return "done"
		}
		result, runErr := shell.Run(ctx, *msg.Command, shell.Options{
			Dir: a.dir, Home: a.dir, Env: shell.Env(a.dir, d.agentName, d.agentUID), User: d.agentCred, Timeout: d.cfg.CommandTimeout,
		})
		content, saved := a.toolContent(runID, n, result, runErr)
		j.Record("result", map[string]any{"call_id": callID, "exit_code": result.ExitCode, "timed_out": result.TimedOut, "output": content, "full_output": saved})
		if ctx.Err() != nil {
			return a.stopReason(ctx)
		}
		messages = append(messages, proto.Message{Role: "tool", CallID: callID, Content: content})
		more, err := a.collectInbox(j)
		if err != nil {
			a.logf("inbox: %v", err)
		}
		var parts []string
		if warnNext {
			parts, warnNext = append(parts, MsgWarn80), false
		}
		if len(more) > 0 {
			parts = append(parts, encodeRecords(more))
		}
		if len(parts) > 0 {
			messages = append(messages, proto.Message{Role: "user", Content: strings.Join(parts, "\n")})
		}
	}
}

// complete calls the provider with the same request until it succeeds.
func (a *agent) complete(ctx context.Context, cfg plugin.Config, system []proto.Segment, messages []proto.Message) (proto.CompleteResponse, error) {
	req := proto.CompleteRequest{Model: cfg.Model, Options: cfg.Options, System: system, Tools: []proto.Tool{proto.ShTool()}, Messages: messages}
	var resp proto.CompleteResponse
	err := a.retry(ctx, func() error {
		var err error
		resp, err = a.d.runner.Complete(ctx, cfg.Provider, req)
		return err
	})
	return resp, err
}

// retry runs call until it succeeds, returns an errConfig, or the agent is
// stopped, with delays doubling from RetryBase to RetryMax. Persistent
// provider failures are reported in ~/ERROR and the log, never the inbox.
func (a *agent) retry(ctx context.Context, call func() error) error {
	delay := a.d.cfg.RetryBase
	var since time.Time
	for failures := 0; ; failures++ {
		err := call()
		var bad errConfig
		if err == nil || errors.As(err, &bad) {
			if failures >= a.d.cfg.ErrorAfter {
				a.clearError(errProviderPrefix)
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failures == 0 {
			since = time.Now().UTC()
		}
		a.logf("provider: %v", err)
		if failures+1 >= a.d.cfg.ErrorAfter {
			text := fmt.Sprintf(MsgProviderFmt, since.Format(time.RFC3339), err)
			a.logf("%s", text)
			a.setError(text)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, a.d.cfg.RetryMax)
	}
}

const (
	errProviderPrefix = "Provider failing since"
	errGuardPrefix    = "Context is over 95%"
)

func (a *agent) setError(text string) {
	if err := a.d.fs.Write(filepath.Join(a.dir, "ERROR"), []byte(text+"\n")); err != nil {
		a.logf("write ERROR: %v", err)
	}
}

// clearError removes ~/ERROR if it holds an error of the given kind.
func (a *agent) clearError(prefix string) {
	path := filepath.Join(a.dir, "ERROR")
	data, _, err := a.d.fs.Read(path, 4096)
	if err == nil && strings.HasPrefix(string(data), prefix) {
		a.d.fs.Remove(path)
	}
}

func seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

// toolContent formats a command result, saving clipped output in full.
func (a *agent) toolContent(runID string, n int, r shell.Result, err error) (string, string) {
	if err != nil {
		return "could not run command: " + err.Error(), ""
	}
	output, clipped := shell.Clip(r.Output, a.d.cfg.OutputClip)
	var content string
	if r.TimedOut {
		content = fmt.Sprintf(MsgTimeoutFmt, seconds(a.d.cfg.CommandTimeout)) + "\n" + output
	} else {
		content = fmt.Sprintf("exit status %d\n%s", r.ExitCode, output)
	}
	if !clipped {
		return content, ""
	}
	dir := filepath.Join(a.dir, ".outputs")
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.txt", runID, n))
	if err := a.d.fs.Mkdir(dir); err == nil {
		err = a.d.fs.Write(path, []byte(r.Output))
	}
	if err != nil {
		a.logf("save output: %v", err)
		return content, ""
	}
	return content + "\n" + fmt.Sprintf(MsgOutputSavedFmt, path), path
}
