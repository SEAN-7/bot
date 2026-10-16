package cli

import (
	"path/filepath"
	"testing"

	"github.com/SEAN-7/bot/internal/layout"
)

func tempLayout(t *testing.T) layout.Layout {
	root := t.TempDir()
	return layout.Layout{Homes: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")}
}
