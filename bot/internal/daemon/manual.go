package daemon

import (
	"os"
	"path/filepath"
)

// writeManual writes the manual directory agents read as ~/manual: each of
// cfg.ManualFiles, atomically, and nothing else. It is outside every agent
// home, so the daemon writes it itself. A failure is logged; the next start
// tries again.
func (d *daemon) writeManual() {
	dir := d.cfg.Manual()
	if err := writeManualDir(dir, d.cfg.ManualFiles); err != nil {
		d.cfg.Logger.Printf("manual %s: %v", dir, err)
	}
}

func writeManualDir(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	for name, text := range files {
		temp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
			os.Remove(temp)
			return err
		}
		if err := os.Chmod(temp, 0o644); err != nil {
			os.Remove(temp)
			return err
		}
		if err := os.Rename(temp, filepath.Join(dir, name)); err != nil {
			os.Remove(temp)
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, ok := files[e.Name()]; !ok {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
