package encoding

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

type nsContextKey struct{}

// NamespaceMode identifies which IEEE 2030.5 XML namespace to use.
type NamespaceMode int

const (
	Namespace2018 NamespaceMode = iota // urn:ieee:std:2030.5:ns (2018/2023)
	Namespace2013                      // http://zigbee.org/sep (2013)
)

// maxMediaRanges bounds per-request parsing cost against an oversized
// Accept header: ranges beyond this are ignored and cannot affect the
// result.
const maxMediaRanges = 32

// DetectNamespace selects the response namespace from the request's Accept
// header line(s). IEEE 2030.5-2018 clause 5.7.2 defines level=-S1/+S1 as
// the 2018 schema and -S0/+S0 (2013) as legacy; -S2/+S2 (2023) is not S1
// but still resolves to 2018, since only an S1 signal suppresses S0. level
// is honored case-insensitively on sep+xml and sep-exi ranges with q!=0
// (the standard requires it only for sep-exi). Unparseable ranges are
// skipped without logging, to avoid a client-controlled log-flood surface.
func DetectNamespace(r *http.Request) NamespaceMode {
	sawS1, sawS0 := false, false
	count := 0

	for _, line := range r.Header.Values("Accept") {
		rest := line
		for rest != "" && count < maxMediaRanges {
			var part string
			part, rest = nextMediaRange(rest)
			count++

			mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil || !isSepMediaType(mediaType) {
				continue
			}
			if q, ok := params["q"]; ok && isZeroQuality(q) {
				continue
			}

			level := params["level"]
			switch {
			case strings.EqualFold(level, "-S1"), strings.EqualFold(level, "+S1"):
				sawS1 = true
			case strings.EqualFold(level, "-S0"), strings.EqualFold(level, "+S0"):
				sawS0 = true
			}
		}
	}

	if sawS0 && !sawS1 {
		return Namespace2013
	}
	return Namespace2018
}

// nextMediaRange returns the next media range from s and the unparsed
// remainder, splitting on a comma unless it falls inside a quoted
// parameter value.
func nextMediaRange(s string) (part, rest string) {
	inQuotes := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				return s[:i], s[i+1:]
			}
		}
	}
	return s, ""
}

func isSepMediaType(mediaType string) bool {
	return mediaType == "application/sep+xml" || mediaType == "application/sep-exi"
}

// isZeroQuality reports whether q marks a range "not acceptable" per RFC
// 9110 12.4.2. A malformed q value is treated as acceptable.
func isZeroQuality(q string) bool {
	v, err := strconv.ParseFloat(q, 64)
	return err == nil && v == 0
}

// nsBufferedWriter buffers the response, rewrites namespace, then flushes.
type nsBufferedWriter struct {
	http.ResponseWriter
	buf     bytes.Buffer
	mode    NamespaceMode
	status  int
	headers http.Header
}

func (w *nsBufferedWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}

func (w *nsBufferedWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *nsBufferedWriter) Write(data []byte) (int, error) {
	return w.buf.Write(data)
}

func (w *nsBufferedWriter) flush() {
	data := w.buf.Bytes()
	data = RewriteNamespace(data, w.mode)

	// Copy buffered headers to real response (except Content-Length which may be wrong)
	realHeader := w.ResponseWriter.Header()
	for k, vals := range w.headers {
		if k == "Content-Length" {
			continue // will be set correctly below
		}
		for _, v := range vals {
			realHeader.Set(k, v)
		}
	}

	// Set correct Content-Length after namespace rewriting
	realHeader.Set("Content-Length", fmt.Sprintf("%d", len(data)))

	if w.status == 0 {
		w.status = 200
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(data)
}

// NamespaceMiddleware detects the client's namespace preference and
// rewrites XML output to match. Supports 2013 and 2018/2023 namespaces.
func NamespaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := DetectNamespace(r)
		ctx := context.WithValue(r.Context(), nsContextKey{}, mode)

		if mode != Namespace2018 {
			bw := &nsBufferedWriter{ResponseWriter: w, mode: mode}
			next.ServeHTTP(bw, r.WithContext(ctx))
			bw.flush()
		} else {
			next.ServeHTTP(w, r.WithContext(ctx))
		}
	})
}

// GetNamespace retrieves the namespace mode from the context.
func GetNamespace(ctx context.Context) NamespaceMode {
	if mode, ok := ctx.Value(nsContextKey{}).(NamespaceMode); ok {
		return mode
	}
	return Namespace2018
}

// RewriteNamespace converts XML from 2018 namespace to 2013 if needed.
func RewriteNamespace(data []byte, mode NamespaceMode) []byte {
	if mode == Namespace2013 {
		return bytes.ReplaceAll(data,
			[]byte(sep2.Namespace),
			[]byte(sep2.Namespace2013))
	}
	return data
}
