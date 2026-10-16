//go:build !unix

package main

import (
	"errors"
	"io/fs"
	"os"
)

var currentUID = os.Getuid

// keyFileUnsupported makes a start without SEP2_ADMIN_KEY refuse here, before
// any file is created: the key file's owner cannot be checked, so creating a
// file that every restart would then refuse is worse than asking for the
// variable up front.
var keyFileUnsupported = errors.New("the admin key file is not supported on this platform: set SEP2_ADMIN_KEY")

var errKeyFileSymlink = errors.New("is a symbolic link")

func openKeyFile(path string) (*os.File, error) { return os.Open(path) }

// fileOwnedByCurrentUser fails closed where file ownership cannot be read.
func fileOwnedByCurrentUser(fs.FileInfo) (bool, error) {
	return false, nil
}
