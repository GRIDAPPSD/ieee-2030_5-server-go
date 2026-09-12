package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestSubscribeStatusError(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusCreated, http.StatusOK} {
		if err := subscribeStatusError(code); err != nil {
			t.Errorf("status %d: err = %v, want nil", code, err)
		}
	}

	// A 400 has several causes, so the loopback opt-in is offered as one of
	// them rather than asserted as the cause.
	msg := subscribeStatusError(http.StatusBadRequest).Error()
	for _, want := range []string{"400", "possible cause", "SEP2_NOTIFICATION_ALLOW_LOOPBACK"} {
		if !strings.Contains(msg, want) {
			t.Errorf("400 message %q does not contain %q", msg, want)
		}
	}
	for _, claim := range []string{"must run", "so the server"} {
		if strings.Contains(msg, claim) {
			t.Errorf("400 message %q asserts the cause (%q)", msg, claim)
		}
	}

	other := subscribeStatusError(http.StatusInternalServerError).Error()
	if !strings.Contains(other, "500") || strings.Contains(other, "SEP2_NOTIFICATION_ALLOW_LOOPBACK") {
		t.Errorf("500 message = %q, want the status and no loopback hint", other)
	}
}
