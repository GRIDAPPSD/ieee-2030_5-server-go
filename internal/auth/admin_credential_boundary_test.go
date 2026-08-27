package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// Every refusal on every branch writes this body, so a caller cannot tell a
// blank configured key from a wrong token from a malformed header.
const refusalBody = `{"error":"admin authentication required"}`

// bearerOnlyMiddleware wires the middleware with no ticket and no session
// store, so the Bearer branch is the only credential branch that can admit.
func bearerOnlyMiddleware(adminKey string) http.Handler {
	return auth.AdminAuthMiddleware(adminKey, nil, nil)(okHandler())
}

// nonLoopbackRequest builds a request the loopback bypass declines, so the
// Bearer branch is the branch under test. httptest.NewRequest already sets a
// documentation-range RemoteAddr; pinning it here keeps that load-bearing.
func nonLoopbackRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "203.0.113.9:41000"
	return req
}

// TestAdminAuthBlankConfiguredKeyRefusesEveryPresentedCredential crosses the
// blank configured keys with the blank presented credentials and asserts every
// cell refuses. Unset, empty and whitespace-only are separate candidates: a
// key of " " must not be authenticated by a caller presenting " ".
func TestAdminAuthBlankConfiguredKeyRefusesEveryPresentedCredential(t *testing.T) {
	t.Parallel()

	configured := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"single space", " "},
		{"tab", "\t"},
		{"newline", "\n"},
		{"mixed whitespace", " \t \n "},
	}

	presented := []struct {
		name   string
		header string // "" means send no Authorization header at all
		send   bool
	}{
		{"no header", "", false},
		{"scheme word with a trailing space", "Bearer ", true},
		{"scheme word alone", "Bearer", true},
		{"empty payload after two spaces", "Bearer  ", true},
		// The fifth cell is built per row so the caller presents the
		// configured key back byte-for-byte. A static header cannot do this:
		// the scheme separator eats one space, so only a derived value makes
		// the presented token equal the configured key.
		{"the configured key echoed back byte-for-byte", "", true},
	}

	if got, want := len(configured)*len(presented), 25; got != want {
		t.Fatalf("credential-boundary table has %d cells (%d configured x %d presented), want %d",
			got, len(configured), len(presented), want)
	}

	for _, cfg := range configured {
		for _, pres := range presented {
			t.Run(cfg.name+"/"+pres.name, func(t *testing.T) {
				t.Parallel()
				h := bearerOnlyMiddleware(cfg.key)
				req := nonLoopbackRequest("/api/devices")
				header := pres.header
				if header == "" && pres.send {
					header = "Bearer " + cfg.key
				}
				if pres.send {
					req.Header.Set("Authorization", header)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)

				if w.Code != http.StatusUnauthorized {
					t.Errorf("configured %q + presented %q: status = %d, want 401",
						cfg.key, header, w.Code)
				}
				if got := w.Body.String(); got != refusalBody {
					t.Errorf("configured %q + presented %q: body = %q, want %q",
						cfg.key, header, got, refusalBody)
				}
			})
		}
	}
}

// TestAdminAuthNonBlankKeyIsByteExact is the refusal in the other direction: a
// configured key that genuinely ends in whitespace is admitted only when the
// caller presents those bytes too. Nothing on the resolution path may trim it.
func TestAdminAuthNonBlankKeyIsByteExact(t *testing.T) {
	t.Parallel()

	const configuredKey = "s3cret "

	cases := []struct {
		name      string
		presented string
		want      int
	}{
		{"presented byte-for-byte with the trailing space", "s3cret ", http.StatusOK},
		{"presented trimmed", "s3cret", http.StatusUnauthorized},
		{"presented with a second trailing space", "s3cret  ", http.StatusUnauthorized},
		{"presented with a leading space instead", " s3cret", http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := bearerOnlyMiddleware(configuredKey)
			req := nonLoopbackRequest("/api/devices")
			req.Header.Set("Authorization", "Bearer "+tc.presented)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Errorf("configured %q, presented %q: status = %d, want %d",
					configuredKey, tc.presented, w.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized && w.Body.String() != refusalBody {
				t.Errorf("presented %q: body = %q, want %q", tc.presented, w.Body.String(), refusalBody)
			}
		})
	}
}

// TestAdminAuthSchemeTokenIsCaseInsensitive pins RFC 7235 section 2.1: the
// scheme token is case-insensitive, so every spelling of "Bearer" reaches the
// same comparison, and a wrong token is still refused on every spelling.
func TestAdminAuthSchemeTokenIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	const configuredKey = "s3cret"

	spellings := []string{"Bearer", "bearer", "BEARER", "BeArEr", "bEARER"}

	for _, scheme := range spellings {
		t.Run(scheme+"/correct token admitted", func(t *testing.T) {
			t.Parallel()
			h := bearerOnlyMiddleware(configuredKey)
			req := nonLoopbackRequest("/api/devices")
			req.Header.Set("Authorization", scheme+" "+configuredKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("scheme %q with the correct token: status = %d, want 200; body = %q",
					scheme, w.Code, w.Body.String())
			}
		})

		t.Run(scheme+"/wrong token refused", func(t *testing.T) {
			t.Parallel()
			h := bearerOnlyMiddleware(configuredKey)
			req := nonLoopbackRequest("/api/devices")
			// Same length as the configured key so the refusal cannot be
			// attributed to the length pre-check alone.
			req.Header.Set("Authorization", scheme+" s3crXt")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("scheme %q with a wrong token: status = %d, want 401", scheme, w.Code)
			}
			if got := w.Body.String(); got != refusalBody {
				t.Errorf("scheme %q wrong token: body = %q, want %q", scheme, got, refusalBody)
			}
		})
	}

	t.Run("a different scheme is not admitted", func(t *testing.T) {
		t.Parallel()
		for _, header := range []string{
			"Basic " + configuredKey,
			"Bearerx " + configuredKey,
			"Bearer" + configuredKey,
		} {
			h := bearerOnlyMiddleware(configuredKey)
			req := nonLoopbackRequest("/api/devices")
			req.Header.Set("Authorization", header)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("header %q: status = %d, want 401", header, w.Code)
			}
		}
	})
}
