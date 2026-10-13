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

// appendConversation adds one record to an agent's conversation log with its
// owner. Only the daemon writes these root-owned files, so their labels can
// be trusted. Each append sets the file's mode and group, so a log left by an
// earlier release is tightened.
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
// NAME; and what root queued for its children in RootHome/.messages/agents/.
// The spool's location and owner identify the sender, so a label cannot be
// forged. Each file is removed only after it was delivered and logged, so a
// crash can only duplicate a message, never lose one.
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
	pass := &spoolPass{ignored: map[string]bool{}, listed: map[string]bool{}, unlisted: map[string]bool{}}
	// Root has no owner, so no spool of its own.
	if uid := d.root.uid(); uid >= 0 {
		d.collectDown(d.root, uid, filepath.Join(d.root.home, ".messages", "agents"), agents, pass)
	} else {
		pass.unlisted[d.root.id] = true
	}
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
	// kept is forgotten only once its spool no longer lists it, whatever
	// became of it this poll. A spool that could not be listed keeps its
	// entries: the file may still be there.
	d.spoolIgnored = pass.ignored
	for k := range d.unremoved {
		if !pass.listed[listedKey(k.agent, k.path)] && !pass.unlisted[k.agent] {
			delete(d.unremoved, k)
		}
	}
	for k := range d.retries {
		if !pass.listed[listedKey(k.agent, k.path)] && !pass.unlisted[k.agent] {
			delete(d.retries, k)
		}
	}
}

// spoolKey names one delivered spool file: its sender, path and content.
type spoolKey struct{ agent, path, sum string }

// spoolPass is what one poll's collection saw.
type spoolPass struct {
	ignored  map[string]bool // entries that are not messages
	listed   map[string]bool // every spool entry seen, by listedKey
	unlisted map[string]bool // agents with a spool that could not be listed
}

func listedKey(agent, path string) string { return agent + "\x00" + path }

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
		return
	}
	delete(d.unremoved, k)
}

// collectUp delivers the agent's messages for its owner: to the parent's
// inbox and the agent's conversation log, or for a child of root, which has
// no inbox, to the log alone.
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
		if a.parent == d.root {
			err = d.appendConversation(d.cfg.ConversationFile(a.id), a.gid(), map[string]any{"from": "agent", "text": logText(data)})
		} else {
			name := filepath.Base(a.ctrl)
			if err = d.deliver(a.parent, d.inboxName(k, name), data, "agents/"+name); err == nil {
				if err := d.appendConversation(d.cfg.ConversationFile(a.id), a.gid(), map[string]any{"from": "agent", "text": logText(data)}); err != nil {
					a.logf("conversation log: %v", err)
				}
			}
		}
		if err != nil {
			if d.deliveryFailed(k) {
				a.logf("message %s: %v", filepath.Base(path), err)
			}
			return
		}
		delete(d.retries, k)
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
		if target := agents[joinID(a.id, e.Name)]; target != nil {
			d.sendDown(a, target, files, pass)
		} else if !a.childWaits(uid, e.Name) {
			for _, f := range files {
				if err := a.fs.Remove(f); err != nil {
					a.logf("message %s: %v", f, err)
					continue
				}
				if a == d.root {
					a.logf(MsgUndeliveredFmt, e.Name) // root has no inbox
				} else if err := d.writeNote(a, "undelivered", fmt.Sprintf(MsgUndeliveredFmt, e.Name)); err != nil {
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
		if err := d.deliver(target, d.inboxName(k, "owner"), data, "owner"); err != nil {
			if d.deliveryFailed(k) {
				a.logf("message %s: %v", path, err)
			}
			return
		}
		delete(d.retries, k)
		if err := d.appendConversation(d.cfg.ConversationFile(target.id), target.gid(), map[string]any{"from": "owner", "text": logText(data)}); err != nil {
			target.logf("conversation log: %v", err)
		}
		d.settle(a, k, pass)
	}
}

// childWaits reports whether a message for a's child name, which is not
// running, should wait: its control directory may yet be admitted (see
// admission), as it holds a user, a home link to a free user's home that
// holds its evidence, or a config.json, or that could not be decided (a
// failure, logged once and retried). Any other child never starts, and its
// messages are dropped. It shares admission's record of the free homes'
// evidence, so a waiting child starts no helper at every poll.
func (a *agent) childWaits(uid int, name string) bool {
	ctrl, id := filepath.Join(a.home, "agents", name), joinID(a.id, name)
	e, err := a.fs.Stat(ctrl)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	} else if err != nil {
		a.d.admitFailed(ctrl, id, "control directory", err)
		return true
	}
	if e.Type != "dir" || e.UID != uid {
		return false
	}
	usable, _, err := a.d.admission(a, ctrl, id, a.d.pool.userOf(id))
	if err != nil {
		a.d.admitFailed(ctrl, id, "admission", err)
		return true
	}
	return usable != nil
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
		path := filepath.Join(dir, e.Name)
		pass.listed[listedKey(a.id, path)] = true
		if strings.HasPrefix(e.Name, ".") || e.Type == "dir" {
			continue
		}
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
