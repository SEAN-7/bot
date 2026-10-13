package cli

import (
	"strings"
	"testing"
)

// Usage says how agent new takes its config, that it returns at once, and
// where the daemon's answer appears. The flags, the wait and the sharing
// recipe are gone: the agent builds those itself.
func TestUsageDescribesNew(t *testing.T) {
	for _, want := range []string{"agent new NAME [CONFIG|-]", "root must pass CONFIG", "agents/NAME/home", "agents/NAME/ERROR"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	for _, gone := range []string{"--provider", "--model", "--option", "10 seconds", "setfacl", "follow"} {
		if strings.Contains(Usage, gone) {
			t.Errorf("usage still has %q", gone)
		}
	}
}
