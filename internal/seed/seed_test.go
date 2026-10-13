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

// The README leaves the sharing recipe to agent help.
func TestSeedReadmeLeavesSharingToHelp(t *testing.T) {
	if strings.Contains(README, "setfacl") {
		t.Error("README has the setfacl recipe")
	}
}
