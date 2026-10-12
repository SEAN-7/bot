package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// messageLimit matches the limit `agent message` enforces.
const messageLimit = 64 << 10

const msgTruncated = "\n[message truncated]"

func logText(data []byte) string { return strings.ToValidUTF8(string(data), "�") }

// appendConversation adds one record to a conversation log: the human's with
// the main agent, or a subagent's with its parent. Only the daemon writes
// these root-owned files, so their labels can be trusted. Each append sets the
// file's mode and group, so a log left by an earlier release is tightened.
func (d *daemon) appendConversation(path string, gid int, record map[string]any) error {
	record["t"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	err = f.Chmod(0o640)
	if err == nil && gid >= 0 {
		err = f.Chown(-1, gid)
	}
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// collectMessages delivers what each admitted agent queued, through its own
// helper: ~/.messages/ for its owner, ~/.messages/agents/NAME/ for its child
// NAME. The spool's location and owner identify the sender, so a label
// cannot be forged. Each file is removed only after it was delivered and
// logged, so a crash can only duplicate a message, never lose one.
func (d *daemon) collectMessages() {
	d.mu.Lock()
	agents := make(map[string]*agent, len(d.agents))
	ids := make([]string, 0, len(d.agents))
	for id, a := range d.agents {
		agents[id] = a
		ids = append(ids, id)
	}
	d.mu.Unlock()
	sort.Strings(ids)
	pass := &spoolPass{ignored: map[string]bool{}, kept: map[spoolKey]bool{}, unlisted: map[string]bool{}}
	for _, id := range ids {
		a := agents[id]
		uid := a.uid()
		if uid < 0 {
			pass.unlisted[a.id] = true
			continue // no user to check ownership against
		}
		spool := filepath.Join(a.home, ".messages")
		d.collectUp(a, uid, spool, pass)
		d.collectDown(a, uid, filepath.Join(spool, "agents"), agents, pass)
	}
	// Remember only what is still there, so a path is reported once while
	// it stays and again if it comes back, and a delivered file the agent
	// kept is forgotten once it is gone. A spool that could not be listed
	// keeps its entries: the file may still be there.
	d.spoolIgnored = pass.ignored
	for k := range d.unremoved {
		if !pass.kept[k] && !pass.unlisted[k.agent] {
			delete(d.unremoved, k)
		}
	}
}

// spoolKey names one delivered spool file: its sender, path and content.
type spoolKey struct{ agent, path, sum string }

// spoolPass is what one poll's collection saw.
type spoolPass struct {
	ignored  map[string]bool   // entries that are not messages
	kept     map[spoolKey]bool // delivered files still in place
	unlisted map[string]bool   // agents with a spool that could not be listed
}

// alreadyDelivered reports whether the spool file at path with this content was
// already delivered but could not be removed. Such a file is not delivered
// again while it stays; its removal is retried.
func (d *daemon) alreadyDelivered(a *agent, path string, data []byte, pass *spoolPass) (spoolKey, bool) {
	sum := sha256.Sum256(data)
	k := spoolKey{a.id, path, hex.EncodeToString(sum[:])}
	if !d.unremoved[k] {
		return k, false
	}
	d.settle(a, k, pass)
	return k, true
}

// settle removes a delivered spool file. One the agent keeps from being
// removed is remembered, and logged once, so it is delivered only once.
func (d *daemon) settle(a *agent, k spoolKey, pass *spoolPass) {
	if err := a.fs.Remove(k.path); err != nil {
		if !d.unremoved[k] {
			a.logf("message %s delivered but not removed: %v", k.path, err)
		}
		d.unremoved[k] = true
		pass.kept[k] = true
		return
	}
	delete(d.unremoved, k)
}

// collectUp delivers the agent's messages for its owner: the human's log for
// the main agent, the parent's inbox for a subagent.
func (d *daemon) collectUp(a *agent, uid int, spool string, pass *spoolPass) {
	for _, path := range d.spoolFiles(a, uid, spool, pass) {
		data, err := readMessage(a, path)
		if err != nil {
			a.logf("message %s: %v", filepath.Base(path), err)
			continue
		}
		k, done := d.alreadyDelivered(a, path, data, pass)
		if done {
			continue
		}
		if a.parent == nil {
			err = d.appendConversation(d.cfg.MessagesFile(), a.gid(), map[string]any{"from": "agent", "agent": a.name, "text": logText(data)})
		} else {
			name := filepath.Base(a.ctrl)
			if err = d.deliver(a.parent, noteName(name, time.Now()), data, "agents/"+name); err == nil {
				if err := d.appendConversation(d.cfg.ConversationFile(a.id), a.gid(), map[string]any{"from": "agent", "text": logText(data)}); err != nil {
					a.logf("conversation log: %v", err)
				}
			}
		}
		if err != nil {
			a.logf("message %s: %v", filepath.Base(path), err)
			return
		}
		d.settle(a, k, pass)
	}
}

// collectDown delivers the agent's messages for its children. Only real
// directories the agent owns, named like a control directory, are spools.
func (d *daemon) collectDown(a *agent, uid int, dir string, agents map[string]*agent, pass *spoolPass) {
	entries, err := a.fs.List(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	} else if err != nil {
		a.logf("messages: %v", err)
		pass.unlisted[a.id] = true
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, e := range entries {
		path := filepath.Join(dir, e.Name)
		if e.Type != "dir" || e.UID != uid || !validControlDir(a.home, filepath.Join(a.home, "agents", e.Name)) {
			d.ignoreSpool(a, path, "not a spool directory the agent owns", pass)
			continue
		}
		files := d.spoolFiles(a, uid, path, pass)
		if len(files) == 0 {
			continue
		}
		if target := agents[filepath.Join(a.id, "agents", e.Name)]; target != nil {
			d.sendDown(a, target, files, pass)
		} else if !a.childWaits(uid, e.Name) {
			for _, f := range files {
				if err := a.fs.Remove(f); err != nil {
					a.logf("message %s: %v", f, err)
					continue
				}
				if err := d.writeNote(a, "undelivered", fmt.Sprintf(MsgUndeliveredFmt, e.Name)); err != nil {
					a.logf("undelivered note: %v", err)
				}
			}
		}
	}
}

// sendDown delivers files from a's spool for target, a's child, as the owner's.
func (d *daemon) sendDown(a, target *agent, files []string, pass *spoolPass) {
	for _, path := range files {
		data, err := readMessage(a, path)
		if err != nil {
			a.logf("message %s: %v", path, err)
			continue
		}
		k, done := d.alreadyDelivered(a, path, data, pass)
		if done {
			continue
		}
		if err := d.deliver(target, noteName("owner", time.Now()), data, "owner"); err != nil {
			a.logf("message %s: %v", path, err)
			return
		}
		if err := d.appendConversation(d.cfg.ConversationFile(target.id), target.gid(), map[string]any{"from": "owner", "text": logText(data)}); err != nil {
			target.logf("conversation log: %v", err)
		}
		d.settle(a, k, pass)
	}
}

// childWaits reports whether a message for a's child name should wait: its
// control directory holds a config.json, so it may yet be admitted. Only a
// child that certainly does not exist has its messages dropped.
func (a *agent) childWaits(uid int, name string) bool {
	ctrl := filepath.Join(a.home, "agents", name)
	for _, p := range []struct{ path, kind string }{{ctrl, "dir"}, {filepath.Join(ctrl, "config.json"), "file"}} {
		e, err := a.fs.Stat(p.path)
		if errors.Is(err, fs.ErrNotExist) {
			return false
		} else if err != nil {
			a.logf("messages for %s: %v", name, err)
			return true
		}
		if e.Type != p.kind || e.UID != uid {
			return false
		}
	}
	return true
}

// spoolFiles lists a spool's messages in name order: regular, non-dot files
// owned by the agent. Anything else that is not a directory is left in place
// and logged.
func (d *daemon) spoolFiles(a *agent, uid int, dir string, pass *spoolPass) []string {
	entries, err := a.fs.List(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		a.logf("messages: %v", err)
		pass.unlisted[a.id] = true
		return nil
	}
	var paths []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") || e.Type == "dir" {
			continue
		}
		path := filepath.Join(dir, e.Name)
		if e.Type != "file" || e.UID != uid {
			d.ignoreSpool(a, path, "not a regular file the agent owns", pass)
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// ignoreSpool logs a spool entry that is not the agent's message, once.
func (d *daemon) ignoreSpool(a *agent, path, why string, pass *spoolPass) {
	if !d.spoolIgnored[path] {
		a.logf("ignoring %s: %s", path, why)
	}
	pass.ignored[path] = true
}

// readMessage reads a spooled message, at most messageLimit bytes of it.
func readMessage(a *agent, path string) ([]byte, error) {
	data, size, err := a.fs.Read(path, messageLimit)
	if err != nil {
		return nil, err
	}
	if size > int64(len(data)) {
		data = append(data, msgTruncated...)
	}
	return data, nil
}
