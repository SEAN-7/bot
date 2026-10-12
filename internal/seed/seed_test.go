package seed

import (
	"strings"
	"testing"
)

func TestSeedReadmeCoversTopics(t *testing.T) {
	for _, want := range []string{"TASK.md", "archive", "journal", "systemd-run --user", "agent provider describe", `"from": "owner"`, "cron", "~/agents/", "dot", "Never poll", "agent message",
		"~/config.json", "~/journal/messages.jsonl", "agent send NAME", "agent new NAME", "agent remove NAME", "agent help",
		"~/agents/NAME/OWNER.md", "~/agents/NAME/ERROR", "~/agents/NAME/home", `"from": "agents/NAME"`} {
		if !strings.Contains(README, want) {
			t.Errorf("README lacks %q", want)
		}
	}
}

// The README names no path that 0.4.0 removed, and leaves the sharing recipe
// to agent help.
func TestSeedReadmeDropsOldLayout(t *testing.T) {
	for _, old := range []string{"/var/log/agent/messages.jsonl", "~/agents/NAME/config.json", "setfacl", "pauses"} {
		if strings.Contains(README, old) {
			t.Errorf("README still has %q", old)
		}
	}
}
