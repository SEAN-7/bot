package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/internal/shell"
)

const (
	segmentLimit = 1 << 20            // README.md as read into the system prompt
	configLimit  = plugin.ConfigLimit // config.json
)

type recorder interface {
	Record(typ string, fields map[string]any) error
	Attach(ext string, data []byte) (string, error)
}

func (a *agent) logf(format string, args ...any) {
	a.d.cfg.Logger.Printf("%s: %s", a.name, fmt.Sprintf(format, args...))
}

// gate reads ~/config.json and checks it against the providers cache: an
// installed provider and one of its models. No plugin runs. A config error
// is reported once per distinct error, and the agent waits until the file
// changes.
func (a *agent) gate() (plugin.Config, proto.DescribeResponse, bool) {
	d := a.d
	var cfg plugin.Config
	var providers map[string]proto.DescribeResponse
	data, size, err := a.fs.Read(filepath.Join(a.home, "config.json"), configLimit)
	if err == nil && size > int64(len(data)) {
		err = errors.New("larger than 1 MiB") // not the JSON error of the part read
	}
	if err == nil {
		cfg, err = plugin.ParseConfig(data)
	}
	if err == nil {
		providers, err = plugin.LoadProviders(d.cfg.ProvidersCache())
	}
	if err == nil {
		err = plugin.Validate(cfg, providers)
	}
	if err != nil {
		text := MsgConfigPrefix + err.Error()
		// Root has no inbox: the service log reports its children's errors.
		if text != a.lastError && a.parent != d.root {
			if err := d.writeNote(a.parent, "subagent-error", fmt.Sprintf(MsgSubagentFmt, a.name, text)); err != nil {
				a.logf("notify parent: %v", err)
			}
		}
		a.reportError(text)
		return cfg, proto.DescribeResponse{}, false
	}
	return cfg, providers[cfg.Provider], true
}

func (a *agent) run(ctx context.Context) {
	cfg, describe, ok := a.gate()
	if !ok {
		return
	}
	owner, err := a.readOwner()
	if err != nil {
		// Running without the owner's instructions would be worse than
		// not running.
		a.reportError(MsgOwnerPrefix + err.Error())
		return
	}
	// A run starting settles whatever ERROR held, this daemon's or an
	// earlier one's.
	a.clearError()
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
	if reason := a.steps(ctx, j, cfg, plugin.ImageTypes(describe, cfg.Model), owner); reason != "" {
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

func (a *agent) steps(ctx context.Context, j *journal.Writer, cfg plugin.Config, imageTypes []string, owner string) string {
	d := a.d
	j.Record("start", map[string]any{"agent": a.id, "config": cfg, "version": d.cfg.Version})
	if err := a.fs.Symlink(d.cfg.JournalDir(a.id), filepath.Join(a.home, "journal")); err != nil {
		a.logf("journal link: %v", err)
	}
	if err := a.fs.Symlink(d.cfg.Manual(), filepath.Join(a.home, "manual")); err != nil {
		a.logf("manual link: %v", err)
	}
	// Where its messages go: its owner's inbox. Root has none.
	if a.parent != d.root {
		if err := a.fs.Symlink(filepath.Join(a.parent.home, "inbox"), filepath.Join(a.home, "owner")); err != nil {
			a.logf("owner link: %v", err)
		}
	}
	system := []proto.Segment{{Text: SystemPrompt + ownerSegment(owner)}, {Text: a.readSegment("README.md")}}
	records, shown, err := a.collectInbox(j, imageTypes)
	if err != nil {
		a.logf("inbox: %v", err)
		j.Record("note", map[string]any{"text": "inbox: " + err.Error()})
		return "error"
	}
	if len(records) == 0 {
		return "empty" // the files vanished after the wake-up; nothing to say
	}
	messages := []proto.Message{{Role: "user", Content: encodeRecords(records), Images: shown}}
	for n := 1; ; n++ {
		resp, err := a.complete(ctx, cfg, system, messages)
		if plugin.IsContextOverflow(err) {
			if n == 1 {
				a.reportError(fmt.Sprintf(MsgOverflowGuardFmt, err))
				return "context-guard"
			}
			if err := d.writeNote(a, "reset", fmt.Sprintf(MsgResetFmt, j.Path())); err != nil {
				a.logf("reset note: %v", err)
			}
			return "context"
		}
		if plugin.IsInvalidRequest(err) {
			a.reportError(fmt.Sprintf(MsgInvalidRequestFmt, err))
			return "invalid"
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
		if n == 1 {
			a.clearError() // a successful call settles the guard and provider errors
		}
		if msg.Command == nil {
			return "done"
		}
		wait := commandWait(msg.Wait, d.cfg.CommandWait, d.cfg.MaxCommandWait)
		result, killed, runErr := a.command(ctx, *msg.Command, wait)
		content := a.toolContent(result, runErr, wait, killed)
		j.Record("result", map[string]any{"call_id": callID, "wait": seconds(wait), "exit_code": result.ExitCode, "timed_out": result.TimedOut, "output": content})
		if ctx.Err() != nil {
			return a.stopReason(ctx)
		}
		messages = append(messages, proto.Message{Role: "tool", CallID: callID, Content: content})
		var more []inboxRecord
		var moreImages []proto.Image
		if !d.draining() { // during a stop, new messages wait for the next daemon
			if more, moreImages, err = a.collectInbox(j, imageTypes); err != nil {
				a.logf("inbox: %v", err)
			}
		}
		if len(more) > 0 {
			messages = append(messages, proto.Message{Role: "user", Content: encodeRecords(more), Images: moreImages})
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

// retry runs call until it succeeds, fails in a way a retry cannot fix, or
// the agent is stopped, with delays doubling from RetryBase to RetryMax.
// Persistent provider failures are reported in ERROR and the log, never the
// inbox.
func (a *agent) retry(ctx context.Context, call func() error) error {
	delay := a.d.cfg.RetryBase
	var since time.Time
	for failures := 0; ; failures++ {
		err := call()
		if err == nil || plugin.IsContextOverflow(err) || plugin.IsInvalidRequest(err) {
			if failures >= a.d.cfg.ErrorAfter {
				a.clearError()
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
			a.reportError(fmt.Sprintf(MsgProviderFmt, since.Format(time.RFC3339), err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, a.d.cfg.RetryMax)
	}
}

// reportError puts text in the agent's ERROR, in its control directory where
// its owner sees it, and in the service log once per distinct text. ERROR
// holds the latest cause; a missing or different file is written again.
func (a *agent) reportError(text string) {
	if text != a.lastError {
		a.lastError = text
		a.logf("not starting: %s", text)
	}
	parent, path := a.errorFile()
	if data, _, err := parent.Read(path, 4096); err == nil && string(data) == text+"\n" {
		return
	}
	if err := parent.Write(path, []byte(text+"\n"), 0o644); err != nil {
		a.logf("write ERROR: %v", err)
	}
}

// clearError removes the agent's ERROR, whatever it holds.
func (a *agent) clearError() {
	a.lastError = ""
	parent, path := a.errorFile()
	parent.Remove(path)
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
// now so that current group memberships apply, then kills whatever it left
// running and returns those processes' names.
func (a *agent) command(ctx context.Context, command string, wait time.Duration) (shell.Result, []string, error) {
	cred, err := a.cred()
	if err != nil {
		return shell.Result{}, nil, fmt.Errorf("look up user %s: %w", a.userName(), err)
	}
	result, err := shell.Run(ctx, command, shell.Options{
		Dir: a.home, Home: a.home, Env: shell.Env(a.home, a.userName(), credUID(cred)), User: cred, Timeout: wait,
	})
	return result, a.endStrays(cred), err
}

// endStrays kills what the last command left running in the daemon's
// service. An agent run without changing identity (in tests) shares the
// daemon's uid, so its processes cannot be told apart from the daemon's
// own; they are left alone.
func (a *agent) endStrays(cred *syscall.Credential) []string {
	if cred == nil || int(cred.Uid) == os.Getuid() {
		return nil
	}
	names, err := killStrays("/proc", int(cred.Uid), a.fs.Pid)
	if err != nil {
		a.logf("processes left by a command: %v", err)
	}
	return names
}

func seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

// toolContent formats a command result, clipping long output.
func (a *agent) toolContent(r shell.Result, err error, wait time.Duration, killed []string) string {
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
	if note := strayNote(killed); note != "" {
		content += "\n" + note
	}
	return content
}
