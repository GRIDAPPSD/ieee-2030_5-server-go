package notification_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/notification"
)

type fakeRecorder struct {
	err  error
	seen []sep2.Notification
}

func (f *fakeRecorder) Record(_ context.Context, n sep2.Notification) error {
	f.seen = append(f.seen, n)
	return f.err
}

const body = `<Notification xmlns="urn:ieee:std:2030.5:ns"><subscribedResource>/r</subscribedResource>` +
	`<subscriptionURI>/s</subscriptionURI><status>4</status></Notification>`

func post(h http.Handler, b string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ntfy", strings.NewReader(b)))
	return w
}

func TestHandlePost(t *testing.T) {
	t.Run("accepted body reaches the recorder with its fields", func(t *testing.T) {
		rec := &fakeRecorder{}
		w := post(notification.HandlePost(rec), body)
		if w.Code != http.StatusOK || w.Header().Get("Location") != "/ntfy" {
			t.Fatalf("status %d Location %q", w.Code, w.Header().Get("Location"))
		}
		if len(rec.seen) != 1 || rec.seen[0].Status != 4 || rec.seen[0].SubscribedResource != "/r" || rec.seen[0].SubscriptionURI != "/s" {
			t.Errorf("recorded %+v", rec.seen)
		}
	})
	t.Run("unknown device is 403", func(t *testing.T) {
		w := post(notification.HandlePost(&fakeRecorder{err: devicelog.ErrUnknownDevice}), body)
		if w.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403", w.Code)
		}
	})
	t.Run("a failed record is 500, not an acceptance", func(t *testing.T) {
		w := post(notification.HandlePost(&fakeRecorder{err: errors.New("store down")}), body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("status %d, want 500", w.Code)
		}
	})
	t.Run("invalid body never reaches the recorder", func(t *testing.T) {
		rec := &fakeRecorder{}
		if w := post(notification.HandlePost(rec), "<x/>"); w.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", w.Code)
		}
		if len(rec.seen) != 0 {
			t.Errorf("recorded %+v", rec.seen)
		}
	})
	t.Run("no recorder is 503", func(t *testing.T) {
		if w := post(notification.HandlePost(nil), body); w.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503", w.Code)
		}
	})
}
