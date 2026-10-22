//go:build unix

package devicelog

import "syscall"

// noFollow makes opening a log file that is a symlink fail instead of
// writing through the link.
const noFollow = syscall.O_NOFOLLOW
