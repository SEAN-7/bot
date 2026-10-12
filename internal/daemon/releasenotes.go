package daemon

import (
	"fmt"
	"strconv"
	"strings"
)

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

// catchUp writes the release notes a has not seen into its inbox, for the
// run that is starting to collect with the rest. Nothing wakes an agent for
// them. An entry without notes, and a development build, have none to
// deliver. A failed write is retried at the next run.
func (d *daemon) catchUp(a *agent) {
	current, ok := parseVersion(d.cfg.Version)
	if !ok {
		return
	}
	e, held := d.pool.entryOf(a.id)
	seen, ok := parseVersion(e.Notes)
	if !held || !ok || !less(seen, current) {
		return
	}
	sections := notesSince(d.cfg.ReleaseNotes, seen, current)
	if len(sections) == 0 {
		return
	}
	text := fmt.Sprintf("The harness was upgraded to v%s. What changed for you:\n\n%s\n", d.cfg.Version, strings.Join(sections, "\n\n"))
	if err := d.writeNote(a, "harness-update", text); err != nil {
		a.logf("release notes: %v", err)
		return
	}
	if err := d.pool.setNotes(e.User, d.cfg.Version); err != nil {
		a.logf("release notes: %v", err)
	}
}
