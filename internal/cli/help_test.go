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
