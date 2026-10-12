package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// deliverReleaseNotes tells the main agent what changed since the version it
// last heard about, once per upgrade. With no record (an install from before
// this existed) it delivers every section up to this version; fresh installs
// write the record at install time, so their seed README is current.
func (d *daemon) deliverReleaseNotes() {
	current, ok := parseVersion(d.cfg.Version)
	if !ok {
		return
	}
	data, err := os.ReadFile(d.cfg.VersionFile())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		d.cfg.Logger.Printf("release notes: %v", err)
		return
	}
	recordedText := strings.TrimSpace(string(data))
	if recordedText == d.cfg.Version {
		return
	}
	recorded, haveRecord := parseVersion(recordedText)
	var sections []string
	for _, section := range strings.Split(d.cfg.ReleaseNotes, "\n## ")[1:] {
		v, ok := parseVersion(strings.TrimSpace(strings.SplitN(section, "\n", 2)[0]))
		if ok && !less(current, v) && (!haveRecord || less(recorded, v)) {
			sections = append(sections, "## "+strings.TrimSpace(section))
		}
	}
	if len(sections) > 0 {
		text := fmt.Sprintf("The harness was upgraded to v%s. What changed for you:\n\n%s\n", d.cfg.Version, strings.Join(sections, "\n\n"))
		if err := d.writeNote(d.main, "harness-update", text); err != nil {
			d.cfg.Logger.Printf("release notes: %v", err)
			return // retried at the next start
		}
	}
	if err := os.MkdirAll(d.cfg.Lib, 0o755); err == nil {
		err = os.WriteFile(d.cfg.VersionFile(), []byte(d.cfg.Version+"\n"), 0o644)
	}
	if err != nil {
		d.cfg.Logger.Printf("release notes: record version: %v", err)
	}
}

func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
