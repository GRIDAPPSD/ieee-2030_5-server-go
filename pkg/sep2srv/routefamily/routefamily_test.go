package routefamily_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

func TestWiredServes(t *testing.T) {
	t.Parallel()

	none := routefamily.Wired(nil)
	for _, p := range []string{"/dcap", "/tm", "/sdev", "/edev"} {
		if !none.Serves(p) {
			t.Errorf("%s needs no store but a nil Wired does not serve it", p)
		}
	}
	for _, p := range []string{"/mup", "/dc", "/upt", "/rt", "/msg", "/rsps"} {
		if none.Serves(p) {
			t.Errorf("%s is anchored but a nil Wired serves it", p)
		}
	}
	if none.Serves("/nosuch") || routefamily.Wired(func(routefamily.Anchor) bool { return true }).Serves("/nosuch") {
		t.Error("a prefix outside the table is served")
	}

	onlyMessaging := routefamily.Wired(func(a routefamily.Anchor) bool { return a == routefamily.AnchorMessagingPrograms })
	if !onlyMessaging.Serves("/msg") || onlyMessaging.Serves("/rsps") {
		t.Errorf("msg=%v rsps=%v, want true and false", onlyMessaging.Serves("/msg"), onlyMessaging.Serves("/rsps"))
	}
}
