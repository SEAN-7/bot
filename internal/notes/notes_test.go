package notes

import (
	"strings"
	"testing"
)

// The 0.3.0 notes tell the main agent what upgrading leaves to it.
func TestNotes030(t *testing.T) {
	start := strings.Index(Text, "## 0.3.0\n")
	end := strings.Index(Text, "## 0.2.1\n")
	if start < 0 || end < start {
		t.Fatal("no 0.3.0 section")
	}
	section := Text[start:end]
	for _, want := range []string{"chmod o-rwx", "setfacl -R -d -m u:USER:rwX,u:$(id -un):rwX", "Users are reused",
		"setfacl -R -x u:USER,d:u:USER", "Cron entries and user timers", `agent send NAME "text"`} {
		if !strings.Contains(section, want) {
			t.Errorf("0.3.0 notes lack %q", want)
		}
	}
}
