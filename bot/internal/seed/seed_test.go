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

// The README carries the sharing facts an agent cannot discover: users are
// reused, and ~ takes no default ACL.
func TestSeedReadmeCarriesSharingFacts(t *testing.T) {
	for _, want := range []string{"setfacl", "dedicated directory", "Pool users are reused", "find DIR -user USER", "setfacl -R -x u:USER,d:u:USER DIR", "setfacl -x u:USER ~", "Never set a default ACL on ~"} {
		if !strings.Contains(README, want) {
			t.Errorf("README lacks %q", want)
		}
	}
}
