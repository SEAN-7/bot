package notes

import (
	"strconv"
	"strings"
	"testing"
)

// Every section is "## X.Y.Z", newest first, so an agent reading ~/manual/notes.md
// finds the newest sections at the top.
func TestHeadingsAreDescendingVersions(t *testing.T) {
	var prev [3]int
	n := 0
	for _, line := range strings.Split(Text, "\n") {
		title, ok := strings.CutPrefix(line, "## ")
		if !ok {
			continue
		}
		var v [3]int
		parts := strings.Split(title, ".")
		if len(parts) != 3 {
			t.Fatalf("heading %q is not a version X.Y.Z", line)
		}
		for i, p := range parts {
			x, err := strconv.Atoi(p)
			if err != nil || x < 0 {
				t.Fatalf("heading %q is not a version X.Y.Z", line)
			}
			v[i] = x
		}
		if n > 0 && !descending(prev, v) {
			t.Fatalf("heading %q does not come after a newer version", line)
		}
		prev, n = v, n+1
	}
	if n == 0 {
		t.Fatal("no sections")
	}
}

// descending reports whether b is an older version than a.
func descending(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return b[i] < a[i]
		}
	}
	return false
}
