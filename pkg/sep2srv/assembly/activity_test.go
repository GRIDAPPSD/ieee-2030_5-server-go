package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// activityPolicy resolves every request to the one identity lfdi, so the
// identity is available wherever the recorder sits, and refuses any request
// carrying X-Test-Deny from inside Wrap, as the ACL does. The only thing
// that can keep a refused request out of the recorder is its position inside
// Wrap, which is what the refusal test pins.
func activityPolicy(lfdi string) assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-Deny") != "" {
					http.Error(w, "refused", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		Identity: func(context.Context) (string, string, bool) { return lfdi, "", true },
	}
}

func activityGet(t *testing.T, h http.Handler, deny bool) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
	if deny {
		req.Header.Set("X-Test-Deny", "1")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRouterRecordsRequestsThatPassWrap(t *testing.T) {
	t.Parallel()
	rec := activity.New()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, nil, activityPolicy("caller-lfdi"), "sfdi", "lfdi", nil)

	if code := activityGet(t, h, false); code != http.StatusOK {
		t.Fatalf("GET /dcap status = %d, want 200", code)
	}
	if _, n, ok := rec.Last("caller-lfdi"); !ok || n != 1 {
		t.Errorf("Last(caller) count = %d ok=%v, want 1 true", n, ok)
	}
}

func TestRouterDoesNotRecordRefusedRequests(t *testing.T) {
	t.Parallel()
	rec := activity.New()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, nil, activityPolicy("refused-lfdi"), "sfdi", "lfdi", nil)

	if code := activityGet(t, h, true); code != http.StatusForbidden {
		t.Fatalf("control: refused request status = %d, want 403", code)
	}
	if _, n, ok := rec.Last("refused-lfdi"); ok || n != 0 {
		t.Errorf("refused request recorded: count = %d ok=%v, want none", n, ok)
	}
	if got := rec.State("refused-lfdi", time.Now(), time.Minute); got != activity.NotSeen {
		t.Errorf("refused device State = %q, want %q", got, activity.NotSeen)
	}
}

func TestRouterWithNilActivityServes(t *testing.T) {
	t.Parallel()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, nil, activityPolicy("caller-lfdi"), "sfdi", "lfdi", nil)
	if code := activityGet(t, h, false); code != http.StatusOK {
		t.Errorf("GET /dcap with no recorder: status = %d, want 200", code)
	}
}
