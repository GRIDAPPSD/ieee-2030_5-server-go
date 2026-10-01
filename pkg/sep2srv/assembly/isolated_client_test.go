package assembly_test

import "net/http"

// isolatedClient returns a client with its own transport. httptest.Server.Close
// calls CloseIdleConnections on http.DefaultTransport, so a request sent
// through the default client can lose its connection when a parallel test
// closes its own server. Keep-alives are off so no idle connection outlives
// the request.
func isolatedClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}
