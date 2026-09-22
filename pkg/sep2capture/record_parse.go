package sep2capture

import (
	"bytes"
	"strconv"
)

// maxIndexedPath bounds how much of a request path the index keeps for
// display (Q4): the exchange's own stored bytes, up to perDirectionCap,
// hold the full path already, so this only trims what the listing carries.
const maxIndexedPath = 256

// firstLine returns b up to (not including) its first line break, or all
// of b if it has none. A captured buffer already ends at whatever framing
// the wire carried, so this never needs to handle more than one CRLF- or
// LF-terminated line.
func firstLine(b []byte) []byte {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return b
	}
	if i > 0 && b[i-1] == '\r' {
		return b[:i-1]
	}
	return b[:i]
}

// parseRequestLine reads method and path off a request's first line
// ("GET /a HTTP/1.1") for the exchange list, without the caller reading
// the stored bytes off disk. A line that does not split into at least two
// space-separated fields yields both fields empty: an unparseable first
// line is exactly the MarkRejectedBeforeHandler case, and Mark already
// says so.
func parseRequestLine(b []byte) (method, path string) {
	parts := bytes.SplitN(firstLine(b), []byte(" "), 3)
	if len(parts) < 2 {
		return "", ""
	}
	p := parts[1]
	if len(p) > maxIndexedPath {
		p = p[:maxIndexedPath]
	}
	return string(parts[0]), string(p)
}

// parseStatusLine reads the numeric status off a response's first line
// ("HTTP/1.1 200 OK"). It returns 0 for a line with no such field:
// MarkNoResponse and MarkIncomplete can both close with no response bytes
// at all.
func parseStatusLine(b []byte) int {
	parts := bytes.SplitN(firstLine(b), []byte(" "), 3)
	if len(parts) < 2 {
		return 0
	}
	n, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return 0
	}
	return n
}
