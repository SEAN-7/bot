package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/internal/shell"
)

const (
	segmentLimit = 1 << 20            // README.md and TASK.md as read into the system prompt
	configLimit  = plugin.ConfigLimit // config.json
)

type recorder interface {
	Record(typ string, fields map[string]any) error
	Attach(ext string, data []byte) (string, error)
}

func (a *agent) logf(format string, args ...any) {
	a.d.cfg.Logger.Printf("%s: %s", a.name, fmt.Sprintf(format, args...))
}

// errConfig marks a problem the agent must fix in config.json, as opposed
// to a provider failure, which is retried.
type errConfig struct{ error }

// gate reads ~/config.json and validates it against the provider's describe.
// A config error is reported once per distinct config.json content and the
// agent waits until the file changes; provider failures are retried.
func (a *agent) gate(ctx context.Context) (plugin.Config, proto.DescribeResponse, bool) {
	d := a.d
	var describe proto.DescribeResponse
	data, size, err := a.fs.Read(filepath.Join(a.home, "config.json"), configLimit)
	if err == nil && size > int64(len(data)) {
		err = errConfigTooLarge // not the JSON error of the part read
	}
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
		if a.parent != d.root { // root has no inbox: the service log reports it
			if err := d.writeNote(a.parent, "subagent-error", fmt.Sprintf(MsgSubagentFmt, a.name, text)); err != nil {
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
	owner, err := a.readOwner()
	if err != nil {
		// Running without the owner's instructions would be worse than
		// not running: log once per distinct problem, and keep it in ERROR
		// while it lasts, though a config error or a person replaced it.
		text := MsgOwnerPrefix + err.Error()
		if text != a.failedOwner {
			a.failedOwner = text
			a.logf("not starting: %s", text)
		}
		a.ensureError(text)
		return
	}
	// Cleared on every safe read, not only after a problem this daemon saw:
	// the error may have been written before a restart.
	a.failedOwner = ""
	a.clearError(MsgOwnerPrefix)
	a.d.catchUp(a)
	jdir := a.d.cfg.JournalDir(a.id)
	if !strings.HasPrefix(jdir, filepath.Clean(a.d.cfg.Log)+"/") {
		a.logf("journal path %q escapes %s", jdir, a.d.cfg.Log)
		return
	}
	j, err := journal.Create(jdir, time.Now(), a.gid())
	if err != nil {
		a.logf("journal: %v", err)
		return
	}
	if reason := a.steps(ctx, j, cfg, plugin.ContextTokens(describe, cfg.Model), plugin.ImageTypes(describe, cfg.Model), owner); reason != "" {
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
	data, _, err := a.fs.Read(filepath.Join(a.home, name), segmentLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	} else if err != nil {
		return fmt.Sprintf("(%s could not be read: %v)", name, err)
	}
	return strings.ToValidUTF8(string(data), "�")
}

func (a *agent) steps(ctx context.Context, j *journal.Writer, cfg plugin.Config, contextTokens int, imageTypes []string, owner string) string {
	d := a.d
	j.Record("start", map[string]any{"agent": a.id, "config": cfg, "context_tokens": contextTokens})
	if err := a.fs.Symlink(d.cfg.JournalDir(a.id), filepath.Join(a.home, "journal")); err != nil {
		a.logf("journal link: %v", err)
	}
	system := []proto.Segment{{Text: SystemPrompt + ownerSegment(owner)}, {Text: a.readSegment("README.md")}, {Text: a.readSegment("TASK.md")}}
	records, shown, err := a.collectInbox(j, imageTypes, d.cfg.ImageBudget)
	if err != nil {
		a.logf("inbox: %v", err)
		j.Record("note", map[string]any{"text": "inbox: " + err.Error()})
		return "error"
	}
	if len(records) == 0 {
		return "empty" // the files vanished after the wake-up; nothing to say
	}
	messages := []proto.Message{{Role: "user", Content: encodeRecords(records), Images: shown}}
	warned, warnNext := false, false
	for n := 1; ; n++ {
		resp, err := a.complete(ctx, cfg, system, messages)
		if plugin.IsContextOverflow(err) {
			if n == 1 {
				text := fmt.Sprintf(MsgOverflowGuardFmt, err)
				a.logf("%s", text)
				a.setError(text)
				return "context-guard"
			}
			if err := d.writeNote(a, "reset", fmt.Sprintf(MsgResetFmt, j.Path())); err != nil {
				a.logf("reset note: %v", err)
			}
			return "context"
		}
		if err != nil {
			return a.stopReason(ctx)
		}
		callID := fmt.Sprintf("call-%d", n)
		msg := proto.Message{Role: "assistant", Text: resp.Text, CallID: callID, Opaque: resp.Opaque}
		if resp.Command != nil && strings.TrimSpace(*resp.Command) != "" {
			msg.Command, msg.Wait = resp.Command, resp.Wait
		}
		messages = append(messages, msg)
		j.Record("assistant", map[string]any{"text": msg.Text, "command": msg.Command, "wait": msg.Wait, "call_id": callID, "usage": resp.Usage, "opaque": msg.Opaque})
		if used := resp.Usage.Total(); contextTokens > 0 && msg.Command != nil {
			switch {
			case used*100 >= 95*contextTokens && n == 1:
				// The run is already too big: a reset note would loop forever.
				text := fmt.Sprintf(MsgGuardFmt, used, contextTokens)
				a.logf("%s", text)
				a.setError(text)
				return "context-guard"
			case used*100 >= 95*contextTokens:
				if err := d.writeNote(a, "reset", fmt.Sprintf(MsgResetFmt, j.Path())); err != nil {
					a.logf("reset note: %v", err)
				}
				return "context"
			case used*100 >= 80*contextTokens && !warned:
				warned, warnNext = true, true
			}
		}
		if n == 1 {
			// A successful call settles errors left by earlier runs or a restart.
			a.clearError(errGuardPrefix)
			a.clearError(errProviderPrefix)
		}
		if msg.Command == nil {
			return "done"
		}
		wait := commandWait(msg.Wait, d.cfg.CommandWait, d.cfg.MaxCommandWait)
		result, runErr := a.command(ctx, *msg.Command, wait)
		content := a.toolContent(result, runErr, wait)
		j.Record("result", map[string]any{"call_id": callID, "wait": seconds(wait), "exit_code": result.ExitCode, "timed_out": result.TimedOut, "output": content})
		if ctx.Err() != nil {
			return a.stopReason(ctx)
		}
		messages = append(messages, proto.Message{Role: "tool", CallID: callID, Content: content})
		var more []inboxRecord
		var moreImages []proto.Image
		if !d.draining() { // during a stop, new messages wait for the next daemon
			if more, moreImages, err = a.collectInbox(j, imageTypes, d.cfg.ImageBudget-imageBytes(messages)); err != nil {
				a.logf("inbox: %v", err)
			}
		}
		var parts []string
		if warnNext {
			parts, warnNext = append(parts, MsgWarn80), false
		}
		if len(more) > 0 {
			parts = append(parts, encodeRecords(more))
		}
		if len(parts) > 0 {
			messages = append(messages, proto.Message{Role: "user", Content: strings.Join(parts, "\n"), Images: moreImages})
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
// provider failures are reported in ERROR and the log, never the inbox.
func (a *agent) retry(ctx context.Context, call func() error) error {
	delay := a.d.cfg.RetryBase
	var since time.Time
	for failures := 0; ; failures++ {
		err := call()
		var bad errConfig
		if err == nil || errors.As(err, &bad) || plugin.IsContextOverflow(err) {
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
	errGuardPrefix    = "Context is over"
)

// setError writes the agent's ERROR in its control directory, where its
// owner sees it.
func (a *agent) setError(text string) {
	parent, path := a.errorFile()
	if err := parent.Write(path, []byte(text+"\n"), 0o644); err != nil {
		a.logf("write ERROR: %v", err)
	}
}

// ensureError writes the agent's ERROR unless it already holds text.
func (a *agent) ensureError(text string) {
	parent, path := a.errorFile()
	if data, _, err := parent.Read(path, 4096); err == nil && string(data) == text+"\n" {
		return
	}
	a.setError(text)
}

// clearError removes the agent's ERROR if it holds an error of the given kind.
func (a *agent) clearError(prefix string) {
	parent, path := a.errorFile()
	data, _, err := parent.Read(path, 4096)
	if err == nil && strings.HasPrefix(string(data), prefix) {
		parent.Remove(path)
	}
}

// commandWait interprets the model's wait argument: seconds as a JSON number,
// rounded down and kept between 1s and limit. Anything else, including no
// argument, means def.
func commandWait(raw json.RawMessage, def, limit time.Duration) time.Duration {
	var v *float64
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return def
	}
	secs := math.Max(math.Floor(*v), 1)
	if secs >= limit.Seconds() {
		return limit
	}
	return time.Duration(secs) * time.Second
}

// command runs one model command in the agent's home as its user, looked up
// now so that current group memberships apply.
func (a *agent) command(ctx context.Context, command string, wait time.Duration) (shell.Result, error) {
	cred, err := a.cred()
	if err != nil {
		return shell.Result{}, fmt.Errorf("look up user %s: %w", a.userName(), err)
	}
	return shell.Run(ctx, command, shell.Options{
		Dir: a.home, Home: a.home, Env: shell.Env(a.home, a.userName(), credUID(cred)), User: cred, Timeout: wait,
	})
}

func seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

// toolContent formats a command result, clipping long output.
func (a *agent) toolContent(r shell.Result, err error, wait time.Duration) string {
	if err != nil {
		return "could not run command: " + err.Error()
	}
	output, clipped := shell.Clip(r.Output, a.d.cfg.OutputClip)
	var content string
	if r.TimedOut {
		content = fmt.Sprintf(MsgTimeoutFmt, seconds(wait), seconds(a.d.cfg.MaxCommandWait)) + "\n" + output
	} else {
		content = fmt.Sprintf("exit status %d\n%s", r.ExitCode, output)
	}
	if clipped {
		content += "\n" + MsgOutputClipped
	}
	return content
}
