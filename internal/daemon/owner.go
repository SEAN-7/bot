package daemon

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// delivered records owner messages moved from the root-only spool into the
// main agent's inbox. A record labels its file "owner" once; a copy of an
// old owner message put back into the inbox is plain data.
type delivered struct {
	path    string
	mu      sync.Mutex
	pending map[string]bool
}

type deliveryRecord struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Consumed bool   `json:"consumed,omitempty"`
}

func deliveryKey(agent, name, sum string) string { return agent + "\x00" + name + "\x00" + sum }

func loadDelivered(path string) (*delivered, error) {
	d := &delivered{path: path, pending: map[string]bool{}}
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
		} else {
			d.pending[key] = true
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

func (d *delivered) add(agent, name, sum string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.append(deliveryRecord{Agent: agent, Name: name, SHA256: sum}); err != nil {
		return err
	}
	d.pending[deliveryKey(agent, name, sum)] = true
	return nil
}

// take reports whether the file was delivered by the owner, consuming the record.
func (d *delivered) take(agent, name, sum string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := deliveryKey(agent, name, sum)
	if !d.pending[key] {
		return false
	}
	delete(d.pending, key)
	if err := d.append(deliveryRecord{Agent: agent, Name: name, SHA256: sum, Consumed: true}); err != nil {
		return false
	}
	return true
}

// deliverOwner moves spooled owner messages into the main agent's inbox. The
// record is written before the inbox file, so a delivered file is always
// recognised; a crash in between can only cause a duplicate delivery.
func (d *daemon) deliverOwner() {
	entries, err := os.ReadDir(d.cfg.OwnerSpool())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.cfg.Logger.Printf("owner spool: %v", err)
		}
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		spooled := filepath.Join(d.cfg.OwnerSpool(), e.Name())
		data, err := os.ReadFile(spooled)
		if err != nil {
			d.cfg.Logger.Printf("owner spool: %v", err)
			continue
		}
		sum := sha256.Sum256(data)
		name := noteName("owner", time.Now())
		if err := d.delivered.add(d.cfg.Home, name, hex.EncodeToString(sum[:])); err != nil {
			d.cfg.Logger.Printf("owner delivery record: %v", err)
			continue
		}
		if err := d.fs.Write(filepath.Join(d.cfg.Home, "inbox", name), data); err != nil {
			d.cfg.Logger.Printf("owner delivery: %v", err)
			continue
		}
		if err := d.appendMessage(map[string]any{"from": "owner", "text": strings.ToValidUTF8(string(data), "\uFFFD")}); err != nil {
			d.cfg.Logger.Printf("messages log: %v", err)
		}
		os.Remove(spooled)
	}
}
