//go:build !linux

package fsio

func undumpable() error { return nil }
