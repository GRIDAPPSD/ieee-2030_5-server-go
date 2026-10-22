//go:build unix

package devicelog

import (
	"errors"
	"syscall"
)

// noFollow makes opening a log file that is a symlink fail instead of
// writing through the link.
const noFollow = syscall.O_NOFOLLOW

// openedSymlink reports that an open with noFollow failed because the path
// is a symlink.
func openedSymlink(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
