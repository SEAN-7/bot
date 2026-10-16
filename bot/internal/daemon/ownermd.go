package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// OwnerHeader introduces the owner's instructions in the first system segment.
const OwnerHeader = "# Owner's instructions\n\nYour owner wrote the following. You cannot change it, and agents you\ncreate do not see it. It takes precedence over everything else in your\ncontext."

// MsgOwnerPrefix starts an ERROR written because OWNER.md is unreadable.
const MsgOwnerPrefix = "OWNER.md: "

// ownerSegment is what follows the system prompt in the first segment:
// nothing for an empty or missing OWNER.md, so the cached prefix is unchanged.
func ownerSegment(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "\n\n" + OwnerHeader + "\n\n" + text
}

// readOwner returns the agent's OWNER.md, in its control directory, read
// through its parent's helper: the directory is the parent's, and the agent
// has no way into it. A missing file is no instructions; a regular file is
// used; anything else, a link included (the helper reads through none), is
// an error and the agent must not start.
func (a *agent) readOwner() (string, error) {
	path := filepath.Join(a.ctrl, "OWNER.md")
	e, err := a.parent.fs.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if e.Type != "file" {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	data, _, err := a.parent.fs.Read(path, segmentLimit)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}
