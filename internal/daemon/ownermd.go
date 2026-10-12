package daemon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SEAN-7/bot/internal/fsio"
)

// OwnerHeader introduces the owner's instructions in the first system segment.
const OwnerHeader = "# Owner's instructions\n\nYour owner wrote the following. You cannot change it, and agents you\ncreate do not see it. It takes precedence over everything else in your\ncontext."

// MsgOwnerPrefix starts an ERROR written because OWNER.md is unsafe or unreadable.
const MsgOwnerPrefix = "OWNER.md: "

// ownerSegment is what follows the system prompt in the first segment:
// nothing for an empty or missing OWNER.md, so the cached prefix is unchanged.
func ownerSegment(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "\n\n" + OwnerHeader + "\n\n" + text
}

// readOwner returns the agent's OWNER.md: /etc/agent/OWNER.md for main, read
// as root, or OWNER.md in a subagent's control directory, read through its
// parent's helper. A missing file is no instructions. One that exists is
// used only if it is a regular file, owned by the owner's user and not
// writable by group or others, in a directory that meets the same rule;
// otherwise the error says why and the agent must not start.
func (a *agent) readOwner() (string, error) {
	var data []byte
	var err error
	if a.parent == nil {
		data, err = readMainOwner(a.d.cfg.OwnerFile(), a.d.cfg.OwnerUID)
	} else {
		data, err = readChildOwner(a.parent.fs, a.parent.uid(), filepath.Join(a.ctrl, "OWNER.md"))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

// readMainOwner reads the main agent's OWNER.md as root. It is outside every
// agent home, so the daemon opens it itself, without following a symlink and
// without blocking on a FIFO.
func readMainOwner(path string, uid int) ([]byte, error) {
	dir := filepath.Dir(path)
	dirInfo, dirErr := os.Lstat(dir)
	if errors.Is(dirErr, fs.ErrNotExist) {
		return nil, dirErr
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	} else if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symlink", path)
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	if dirErr != nil {
		return nil, dirErr
	}
	if err := checkSafe(dir, dirInfo.IsDir(), "a directory", fileUID(dirInfo), uid, uint32(dirInfo.Mode().Perm())); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkSafe(path, info.Mode().IsRegular(), "a regular file", fileUID(info), uid, uint32(info.Mode().Perm())); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, segmentLimit))
}

// readChildOwner reads a subagent's OWNER.md through its parent's helper:
// the control directory belongs to the parent, who wrote the file.
func readChildOwner(parent *fsio.Client, uid int, path string) ([]byte, error) {
	dir := filepath.Dir(path)
	file, err := parent.Stat(path)
	if err != nil {
		return nil, err // a missing file is no instructions, whatever the directory
	}
	if uid < 0 {
		return nil, fmt.Errorf("%s: cannot look up its owner's user", path)
	}
	d, err := parent.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("%s: directory vanished", path)
		}
		return nil, err
	}
	if err := checkSafe(dir, d.Type == "dir", "a directory", d.UID, uid, d.Mode); err != nil {
		return nil, err
	}
	if file.Type == "symlink" {
		return nil, fmt.Errorf("%s is a symlink", path)
	}
	if err := checkSafe(path, file.Type == "file", "a regular file", file.UID, uid, file.Mode); err != nil {
		return nil, err
	}
	data, _, err := parent.Read(path, segmentLimit)
	return data, err
}

// checkSafe applies the OWNER.md rule to the file or its directory.
func checkSafe(path string, rightType bool, kind string, owner, uid int, perm uint32) error {
	switch {
	case !rightType:
		return fmt.Errorf("%s is not %s", path, kind)
	case owner != uid:
		return fmt.Errorf("%s is not owned by its owner", path)
	case perm&0o022 != 0:
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}

func fileUID(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}
