package daemon

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// delivered records the messages the daemon put into inbox files, with
// their sender. A record labels its file once; a copy of an old message put
// back into the inbox is plain data.
type delivered struct {
	path    string
	mu      sync.Mutex
	pending map[string]string // delivery key → sender
}

type deliveryRecord struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	From     string `json:"from,omitempty"` // "" in records from earlier versions: the owner
	Consumed bool   `json:"consumed,omitempty"`
}

func deliveryKey(agent, name, sum string) string { return agent + "\x00" + name + "\x00" + sum }

func loadDelivered(path string) (*delivered, error) {
	d := &delivered{path: path, pending: map[string]string{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var r deliveryRecord
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		key := deliveryKey(r.Agent, r.Name, r.SHA256)
		if r.Consumed {
			delete(d.pending, key)
		} else if r.From == "" {
			d.pending[key] = "owner"
		} else {
			d.pending[key] = r.From
		}
	}
	return d, scanner.Err()
}

func (d *delivered) append(r deliveryRecord) error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(r)
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// add records a delivery, unless the same one is already pending: a retry
// of a delivery that failed after it was recorded.
func (d *delivered) add(agent, name, sum, from string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending[deliveryKey(agent, name, sum)] == from {
		return nil
	}
	if err := d.append(deliveryRecord{Agent: agent, Name: name, SHA256: sum, From: from}); err != nil {
		return err
	}
	d.pending[deliveryKey(agent, name, sum)] = from
	return nil
}

// take returns the sender of a file the daemon delivered, consuming the
// record, or "" when it did not deliver this file.
func (d *delivered) take(agent, name, sum string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := deliveryKey(agent, name, sum)
	from := d.pending[key]
	if from == "" {
		return ""
	}
	delete(d.pending, key)
	if err := d.append(deliveryRecord{Agent: agent, Name: name, SHA256: sum, Consumed: true}); err != nil {
		return ""
	}
	return from
}

// deliver writes data to the recipient's inbox through its helper, recording
// the sender first: the agent may collect the file the moment it appears,
// and a crash in between can only cause a duplicate delivery.
func (d *daemon) deliver(to *agent, name string, data []byte, from string) error {
	sum := sha256.Sum256(data)
	if err := d.delivered.add(to.id, name, hex.EncodeToString(sum[:]), from); err != nil {
		return fmt.Errorf("delivery record: %w", err)
	}
	return to.fs.Write(filepath.Join(to.home, "inbox", name), data)
}

// retry is a spool file whose delivery failed.
type retry struct {
	name   string // the inbox name every attempt uses
	logged bool   // its failure was logged
}

// inboxName is the inbox name for delivering spool file k: a new one, or the
// one its failed attempts used, so a retry matches their delivery record.
func (d *daemon) inboxName(k spoolKey, kind string) string {
	if r := d.retries[k]; r != nil {
		return r.name
	}
	name := noteName(kind, time.Now())
	d.retries[k] = &retry{name: name}
	return name
}

// deliveryFailed reports whether to log a failed delivery of spool file k:
// once while it keeps failing. (The errors differ: each attempt writes a
// new temporary file.)
func (d *daemon) deliveryFailed(k spoolKey) bool {
	r := d.retries[k]
	if r == nil {
		return true
	}
	first := !r.logged
	r.logged = true
	return first
}

// ownerSpoolAgent keys the human's spool files in retries.
const ownerSpoolAgent = ""

// deliverOwner moves the human's spooled messages into the main agent's inbox.
func (d *daemon) deliverOwner() {
	entries, err := os.ReadDir(d.cfg.OwnerSpool())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.cfg.Logger.Printf("owner spool: %v", err)
		}
		return
	}
	listed := map[string]bool{}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		spooled := filepath.Join(d.cfg.OwnerSpool(), e.Name())
		listed[spooled] = true
		data, err := os.ReadFile(spooled)
		if err != nil {
			d.cfg.Logger.Printf("owner spool: %v", err)
			continue
		}
		sum := sha256.Sum256(data)
		k := spoolKey{ownerSpoolAgent, spooled, hex.EncodeToString(sum[:])}
		if err := d.deliver(d.main, d.inboxName(k, "owner"), data, "owner"); err != nil {
			if d.deliveryFailed(k) {
				d.cfg.Logger.Printf("owner delivery: %v", err)
			}
			continue
		}
		delete(d.retries, k)
		if err := d.appendConversation(d.cfg.MessagesFile(), d.main.gid(), map[string]any{"from": "owner", "text": logText(data)}); err != nil {
			d.cfg.Logger.Printf("messages log: %v", err)
		}
		os.Remove(spooled)
	}
	// Forget the names kept for files no longer there.
	for k := range d.retries {
		if k.agent == ownerSpoolAgent && !listed[k.path] {
			delete(d.retries, k)
		}
	}
}
