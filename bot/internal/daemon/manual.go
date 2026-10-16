package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SEAN-7/bot/internal/journal"
)

// writeManual writes the manual directory agents read as ~/manual: each of
// cfg.ManualFiles, atomically, and nothing else. It is outside every agent
// home, so the daemon writes it itself. A failure is logged; the next start
// tries again.
func (d *daemon) writeManual() {
	dir := d.cfg.Manual()
	if err := writeManualDir(dir, d.cfg.ManualFiles); err != nil {
		d.cfg.Logger.Printf("manual %s: %v", dir, err)
	}
}

func writeManualDir(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	for name, text := range files {
		temp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
			os.Remove(temp)
			return err
		}
		if err := os.Chmod(temp, 0o644); err != nil {
			os.Remove(temp)
			return err
		}
		if err := os.Rename(temp, filepath.Join(dir, name)); err != nil {
			os.Remove(temp)
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, ok := files[e.Name()]; !ok {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// lastDeliveredNotes is the last release that delivered release notes to
// the inbox. A run that recorded no version ran on 0.5.2 to 0.5.8, so the
// first upgrade note it gets covers everything after this.
const lastDeliveredNotes = "0.5.1"

// upgradeNote writes the release notes a has not seen into its inbox, for
// the run that is starting to collect with the rest, when the harness is a
// newer release than the one that ran a's previous run. Nothing wakes an
// agent for it, and the journal is the only state: the note is written
// before the run's start record, so a crash between them repeats it.
func (d *daemon) upgradeNote(a *agent, jdir string) {
	current, ok := parseVersion(d.cfg.Version)
	if !ok {
		return // a development build
	}
	prev, found, err := journal.LastVersion(jdir)
	if err != nil {
		a.logf("upgrade note: %v", err)
		return
	}
	if !found {
		return // never ran: its seed README is current
	}
	var text string
	if prev == "" {
		seen, _ := parseVersion(lastDeliveredNotes)
		sections := notesSince(d.cfg.ReleaseNotes, seen, current)
		if len(sections) == 0 {
			return
		}
		text = fmt.Sprintf(MsgUpgradeSinceFmt, d.cfg.Version, lastDeliveredNotes, strings.Join(sections, "\n\n"))
	} else {
		seen, ok := parseVersion(prev)
		if !ok || !less(seen, current) {
			return // a development build ran it, the same version, or a downgrade
		}
		sections := notesSince(d.cfg.ReleaseNotes, seen, current)
		if len(sections) == 0 {
			return
		}
		text = fmt.Sprintf(MsgUpgradeFmt, prev, d.cfg.Version, strings.Join(sections, "\n\n"))
	}
	if err := d.writeNote(a, "harness-upgrade", text); err != nil {
		a.logf("upgrade note: %v", err)
	}
}

// parseVersion and less compare release versions, "X.Y.Z".
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

// notesSince returns the "## X.Y.Z" sections of all with seen < X.Y.Z <=
// current, each starting with its "## " line, in file order.
func notesSince(all string, seen, current [3]int) []string {
	var out []string
	parts := strings.Split("\n"+all, "\n## ")
	for _, part := range parts[1:] {
		head, _, _ := strings.Cut(part, "\n")
		if v, ok := parseVersion(strings.TrimSpace(head)); ok && less(seen, v) && !less(current, v) {
			out = append(out, "## "+strings.TrimRight(part, " \t\r\n"))
		}
	}
	return out
}
