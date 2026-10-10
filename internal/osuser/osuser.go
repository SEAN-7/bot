// Package osuser resolves a user name to process credentials.
package osuser

import (
	"os/user"
	"strconv"
	"syscall"
)

// Lookup returns the uid, gid and supplementary groups of name. An empty name
// means "the current user" and returns nil: run without changing identity.
func Lookup(name string) (*syscall.Credential, error) {
	if name == "" {
		return nil, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, err
	}
	cred := &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	ids, err := u.GroupIds()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		g, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return nil, err
		}
		cred.Groups = append(cred.Groups, uint32(g))
	}
	return cred, nil
}
