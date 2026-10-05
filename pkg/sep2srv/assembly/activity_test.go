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

type lfdiKey struct{}

// activityPolicy identifies a caller by the X-Test-LFDI header and refuses,
// as the ACL would, any request carrying X-Test-Deny. Both run in Wrap, so
// the recorder under test sits inside them exactly as in production.
func activityPolicy() assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-Deny") != "" {
					http.Error(w, "refused", http.StatusForbidden)
					return
				}
				ctx := context.WithValue(r.Context(), lfdiKey{}, r.Header.Get("X-Test-LFDI"))
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		},
		Identity: func(ctx context.Context) (string, string, bool) {
			v, _ := ctx.Value(lfdiKey{}).(string)
			return v, "", v != ""
		},
	}
}

func activityGet(t *testing.T, h http.Handler, lfdi string, deny bool) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
	req.Header.Set("X-Test-LFDI", lfdi)
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
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, nil, activityPolicy(), "sfdi", "lfdi", nil)

	if code := activityGet(t, h, "caller-lfdi", false); code != http.StatusOK {
		t.Fatalf("GET /dcap status = %d, want 200", code)
	}
	if _, n, ok := rec.Last("caller-lfdi"); !ok || n != 1 {
		t.Errorf("Last(caller) count = %d ok=%v, want 1 true", n, ok)
	}
}

func TestRouterDoesNotRecordRefusedRequests(t *testing.T) {
	t.Parallel()
	rec := activity.New()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, nil, activityPolicy(), "sfdi", "lfdi", nil)

	if code := activityGet(t, h, "refused-lfdi", true); code != http.StatusForbidden {
		t.Fatalf("control: refused request status = %d, want 403", code)
	}
	if _, n, ok := rec.Last("refused-lfdi"); ok || n != 0 {
		t.Errorf("refused request recorded: count = %d ok=%v, want none", n, ok)
	}
	if got := rec.State("refused-lfdi", timeNow(), 0); got != activity.NotSeen {
		t.Errorf("refused device State = %q, want %q", got, activity.NotSeen)
	}
}

func TestRouterWithNilActivityServes(t *testing.T) {
	t.Parallel()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, nil, activityPolicy(), "sfdi", "lfdi", nil)
	if code := activityGet(t, h, "caller-lfdi", false); code != http.StatusOK {
		t.Errorf("GET /dcap with no recorder: status = %d, want 200", code)
	}
}

func timeNow() time.Time { return time.Now() }
