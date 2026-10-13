package cli

import (
	"strings"
	"testing"
)

// Granting x on a directory exposes its other-readable entries, files a
// subagent creates later need a default ACL, and users are reused: agent help
// carries the recipe the seed README leaves out.
func TestSharingHelpRecipe(t *testing.T) {
	for _, want := range []string{"stat -L -c %U ~/agents/NAME/home", "chmod o-rwx", "setfacl -R -d -m u:USER:rwX,u:$(id -un):rwX", "Users are reused",
		"setfacl -R -x u:USER,d:u:USER", "setfacl -x u:USER ~ ~/share", "find ~/share/NAME -user USER"} {
		if !strings.Contains(SharingHelp, want) {
			t.Errorf("agent help lacks %q", want)
		}
	}
}

// Usage says what agent new does when the daemon is slow, and agent new --help
// has its own text.
func TestUsageDescribesNewTimeout(t *testing.T) {
	for _, text := range []string{Usage, NewUsage} {
		if !strings.Contains(text, "10 seconds") || !strings.Contains(text, "has not admitted") {
			t.Errorf("usage lacks the admission timeout:\n%s", text)
		}
	}
	if !strings.HasPrefix(NewUsage, "Usage: agent new NAME") {
		t.Errorf("NewUsage starts %q", NewUsage)
	}
}

// The help says which directories the harness keeps closed to others.
func TestSharingHelpNamesClosedDirectories(t *testing.T) {
	for _, want := range []string{"~/inbox, ~/agents, ~/.messages and ~/.outputs", "0750"} {
		if !strings.Contains(SharingHelp, want) {
			t.Errorf("agent help lacks %q", want)
		}
	}
}
