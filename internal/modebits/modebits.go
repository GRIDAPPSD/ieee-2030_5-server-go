// Package modebits is the one owner of whether a unix permission-bit check
// can apply on this platform. Go reports every Windows directory as mode
// 0777 and access there is governed by ACLs, so a check on those bits would
// refuse a safe directory or pass an unsafe one.
package modebits

import (
	"log"
	"runtime"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
)

var noted sync.Map

// Available reports whether permission bits mean anything on this platform,
// silently. Tests use it to decide whether to skip a mode assertion.
func Available() bool { return runtime.GOOS != "windows" }

// Applies reports whether permission-bit checks mean anything here. Where
// they do not it logs one line per check and directory saying the check is
// skipped, so a skip is never a silent pass.
func Applies(check, dir string) bool {
	return appliesOn(runtime.GOOS, check, dir)
}

func appliesOn(goos, check, dir string) bool {
	if goos != "windows" {
		return true
	}
	if _, seen := noted.LoadOrStore(check+"\x00"+dir, struct{}{}); !seen {
		log.Printf("%s skipped on %s for %s: unix permission bits do not describe access here; restrict that directory with ACLs", logsafe.Str(check), logsafe.Str(goos), logsafe.Str(dir))
	}
	return false
}
