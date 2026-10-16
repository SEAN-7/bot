package cli

import (
	"strings"
	"testing"
)

// Usage lists only what needs the binary and points to the manual for the
// rest.
func TestUsage(t *testing.T) {
	for _, want := range []string{"agent provider install NAME SOURCE", "agent daemon", "agent help", "/var/lib/agent/manual/manual.md"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	for _, gone := range []string{"agent new", "agent send", "agent message", "agent log", "agent version", "provider describe"} {
		if strings.Contains(Usage, gone) {
			t.Errorf("usage still has %q", gone)
		}
	}
}
