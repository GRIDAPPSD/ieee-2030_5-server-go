package server

import (
	"fmt"
	"strings"
)

// RenderRoutesLog produces the IEEE-140 boot-time route enumeration
// block. One line per listener with its bind address, then each
// pattern indented underneath. The admin section is omitted entirely
// when the listener is disabled (empty addr / nil routes). Pure
// function so tests can pin the output shape without re-running boot.
//
// Defense-in-depth observability: the /api/certs/* mis-mount that
// became Leon CRITICAL on PR #246 was hard to spot by reading
// router.go. Surfacing every route here at startup means any future
// duplicate / cross-listener mount lands in the boot log on first
// run.
func RenderRoutesLog(protocolAddr string, protocolRoutes []string, adminAddr string, adminRoutes []string) string {
	var b strings.Builder
	b.WriteString("Routes mounted:\n")
	fmt.Fprintf(&b, "  protocol (%s):\n", protocolAddr)
	if len(protocolRoutes) == 0 {
		b.WriteString("    (none)\n")
	} else {
		for _, r := range protocolRoutes {
			fmt.Fprintf(&b, "    %s\n", r)
		}
	}
	if adminAddr != "" {
		fmt.Fprintf(&b, "  admin (%s):\n", adminAddr)
		if len(adminRoutes) == 0 {
			b.WriteString("    (none)\n")
		} else {
			for _, r := range adminRoutes {
				fmt.Fprintf(&b, "    %s\n", r)
			}
		}
	}
	return b.String()
}
