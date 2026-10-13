package fsio

import "syscall"

// undumpable stops the agent user, which shares the helper's uid, from
// ptracing it or reading its /proc entries to forge responses.
func undumpable() error {
	const prSetDumpable = 4
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
