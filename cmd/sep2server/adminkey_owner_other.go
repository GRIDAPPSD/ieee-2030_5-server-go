//go:build !unix

package main

import (
	"io/fs"
	"os"
)

var currentUID = os.Getuid

// fileOwnedByCurrentUser fails closed where file ownership cannot be read.
func fileOwnedByCurrentUser(fs.FileInfo) (bool, error) {
	return false, nil
}
