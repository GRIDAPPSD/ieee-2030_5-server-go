package server

import (
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = w.Write([]byte(`{"error":"unsupported content type"}`))
	})
}

// declaresMediaType reports whether a Content-Type header names one of types,
// ignoring parameters such as charset or boundary.
func declaresMediaType(contentType string, types []string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && slices.Contains(types, mediaType)
}
