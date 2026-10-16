package seed

import (
	"strings"
	"testing"
)

// The seed README points to the manual, which holds the facts, and names no
// file the harness no longer loads.
func TestSeedReadme(t *testing.T) {
	for _, want := range []string{"~/manual/manual.md", "~/manual/notes.md", "This file is yours"} {
		if !strings.Contains(README, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	if strings.Contains(README, "TASK.md") {
		t.Error("README names TASK.md")
	}
}
