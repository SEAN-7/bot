package daemon

import "testing"

func TestManualHasVersion(t *testing.T) {
	cfg := DefaultConfig("")
	if v := cfg.ManualFiles["version"]; v != cfg.Version+"\n" {
		t.Fatalf("version file %q", v)
	}
}
