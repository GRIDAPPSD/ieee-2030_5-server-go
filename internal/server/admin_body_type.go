package server

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"slices"
)

// adminBodyTypes names the media types each state-changing route on the
// authenticated admin mux decodes; nil marks a route that reads no body. The
// handlers decode whatever arrives, and text/plain is a type a page can send
// with no preflight, so the label is checked here (#416).
var adminBodyTypes = map[string][]string{
	"POST /api/certs/server":                  {"application/json"},
	"POST /api/certs/device":                  {"application/json"},
	"POST /api/certs/info":                    {"application/x-pem-file", "multipart/form-data"},
	"POST /api/devices":                       {"application/json"},
	"POST /api/fsas":                          {"application/json"},
	"POST /api/fsas/{id}/programs":            {"application/json"},
	"POST /api/devices/{id}/fsa-assignment":   {"application/json"},
	"DELETE /api/fsas/{id}":                   nil,
	"DELETE /api/fsas/{id}/programs":          nil,
	"DELETE /api/devices/{id}/fsa-assignment": nil,
	"POST /auth/ticket":                       nil,
}

// unsupportedContentTypeBody is the 415 refusal shape. Accepted names the
// route's declared type(s) so a caller sees what to send instead of guessing
// from a bare refusal; it is omitted (via omitempty) for a route missing
// from adminBodyTypes, which has no declared type to report (#416).
type unsupportedContentTypeBody struct {
	Error    string   `json:"error"`
	Accepted []string `json:"accepted,omitempty"`
}

// requireAdminBodyTypes refuses a state-changing request whose Content-Type
// is not declared for the route mux matched. A write route missing from
// adminBodyTypes is refused as well, so a new route cannot skip the check.
func requireAdminBodyTypes(mux *recordingMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			mux.ServeHTTP(w, r)
			return
		}
		_, pattern := mux.mux.Handler(r)
		if pattern == "" {
			// No route matched: the mux answers 404 or 405 itself.
			mux.ServeHTTP(w, r)
			return
		}
		types, declared := adminBodyTypes[pattern]
		if declared && (types == nil || declaresMediaType(r.Header.Get("Content-Type"), types)) {
			mux.ServeHTTP(w, r)
			return
		}
		// The prior line on this path was the auth chain's own admission
		// event, identical whether the write then succeeded or (as here)
		// was refused for its content type; this refusal needs its own
		// line, not just headers or the credential (#416).
		slog.Warn("admin: content type refused",
			"event", "admin_body_type_refused",
			"method", r.Method,
			"path", r.URL.Path,
			"pattern", pattern,
			"remote_addr", r.RemoteAddr,
		)
		writeUnsupportedContentType(w, types)
	})
}

// writeUnsupportedContentType writes the 415 refusal body. json.Marshal
// cannot fail on this fixed shape of strings; the fallback keeps the
// refusal itself from being lost to an unchecked encode error.
func writeUnsupportedContentType(w http.ResponseWriter, accepted []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnsupportedMediaType)
	body, err := json.Marshal(unsupportedContentTypeBody{Error: "unsupported content type", Accepted: accepted})
	if err != nil {
		body = []byte(`{"error":"unsupported content type"}`)
	}
	_, _ = w.Write(body)
}

// declaresMediaType reports whether a Content-Type header names one of types,
// ignoring parameters such as charset or boundary.
func declaresMediaType(contentType string, types []string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && slices.Contains(types, mediaType)
}
