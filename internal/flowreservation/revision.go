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
	if base, k, ok := splitRevision(prev); ok {
		return base + "-r" + strconv.Itoa(k+1)
	}
	return prev + "-r1"
}

// RequestIDOf is the id of the request whose chain holds the response
// stored under responseID.
func RequestIDOf(responseID string) string {
	if base, _, ok := splitRevision(responseID); ok {
		return base
	}
	return responseID
}

// splitRevision splits "<base>-r<k>" for a canonical k of at least 1.
func splitRevision(id string) (base string, k int, ok bool) {
	i := strings.LastIndex(id, "-r")
	if i < 0 {
		return "", 0, false
	}
	n := id[i+len("-r"):]
	k, err := strconv.Atoi(n)
	if err != nil || k <= 0 || n != strconv.Itoa(k) {
		return "", 0, false
	}
	return id[:i], k, true
}
