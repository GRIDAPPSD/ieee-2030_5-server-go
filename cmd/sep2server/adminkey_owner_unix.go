//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// currentUID is a variable so tests can simulate a file owned by someone else.
var currentUID = os.Getuid

// keyFileUnsupported is nil where the key file's owner can be checked.
var keyFileUnsupported error

var errKeyFileSymlink = errors.New("is a symbolic link")

// openKeyFile opens path read-only without following a final symlink. The
// non-blocking flag keeps a FIFO from stalling the open; the caller's
// regular-file check then refuses it.
func openKeyFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
		return nil, errKeyFileSymlink
	}
	return f, err
}

// fileOwnedByCurrentUser reports whether fi's owner is the running user.
func fileOwnedByCurrentUser(fi fs.FileInfo) (bool, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("no owner information for the file")
	}
	return int(st.Uid) == currentUID(), nil
}
