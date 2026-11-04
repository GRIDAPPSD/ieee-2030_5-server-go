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

// Applies reports whether permission-bit checks mean anything here. Where
// they do not it logs one line per check name per process saying the check
// is skipped, so a skip is never a silent pass.
func Applies(check string) bool {
	return appliesOn(runtime.GOOS, check)
}

func appliesOn(goos, check string) bool {
	if goos != "windows" {
		return true
	}
	if _, seen := noted.LoadOrStore(check, struct{}{}); !seen {
		log.Printf("%s skipped on %s: unix permission bits do not describe access here; restrict the directory with ACLs", logsafe.Str(check), logsafe.Str(goos))
	}
	return false
}
