package flowreservation

import (
	"strconv"
	"strings"
)

// RevisionID is the store id of the response that revises the one stored
// under prev. The queue stores a request's first response under the
// request's own id, so a revision (#668) needs another: "<request id>-r1",
// then "-r2" and on. Only a live response can be revised, so each request
// has one chain and no two revisions share an id. Request ids are
// "frq-<digits>", which never end in a revision suffix.
func RevisionID(prev string) string {
	if i := strings.LastIndex(prev, "-r"); i >= 0 {
		n := prev[i+len("-r"):]
		if k, err := strconv.Atoi(n); err == nil && k > 0 && n == strconv.Itoa(k) {
			return prev[:i] + "-r" + strconv.Itoa(k+1)
		}
	}
	return prev + "-r1"
}
