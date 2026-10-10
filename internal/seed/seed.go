// Package seed writes the starting files of a new agent directory.
package seed

import (
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed README.md
var README string

//go:embed TASK.md
var TASK string

// Write creates dir's starting files, keeping any that already exist.
func Write(dir string, configJSON []byte) error {
	for _, sub := range []string{"inbox/processed", "agents"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	for name, data := range map[string][]byte{"README.md": []byte(README), "TASK.md": []byte(TASK), "config.json": configJSON} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
