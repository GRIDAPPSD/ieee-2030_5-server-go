package assembly_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, string, uint8) {}

// The response list advertises subscribable only when something can notify
// its subscribers and subscriptions can be created (#669), read from the
// bytes the assembled router serves.
func TestFlowReservationResponseList_SubscribableFollowsTheWiring(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		notifier assembly.ResourceNotifier
		subs     bool
		want     bool
	}{
		{"notifier and subscription store", noopNotifier{}, true, true},
		{"notifier, no subscription store", noopNotifier{}, false, false},
		{"subscription store, no notifier", nil, true, false},
		{"neither", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := testStores()
			if !tc.subs {
				stores.Subscriptions = nil
			}
			seedOwnedDevices(t, stores.EndDevices, "e1")
			handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), testSFDI, testLFDI, tc.notifier)
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			resp, err := srv.Client().Get(srv.URL + "/edev/e1/frp")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /edev/e1/frp: status %d, err %v", resp.StatusCode, err)
			}
			if has := strings.Contains(string(body), `subscribable="1"`); has != tc.want {
				t.Errorf("body has subscribable=\"1\": %v, want %v\n%s", has, tc.want, body)
			}
		})
	}
}
