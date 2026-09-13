package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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

func TestClientSubscribeFuncReportsStatus(t *testing.T) {
	t.Parallel()

	type seen struct {
		path string
		body string
	}
	for _, tc := range []struct {
		status  int
		wantErr string
	}{
		{http.StatusCreated, ""},
		{http.StatusBadRequest, "possible cause"},
	} {
		got := make(chan seen, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			got <- seen{r.URL.Path, string(body)}
			w.WriteHeader(tc.status)
		}))
		subscribe := newClientSubscribeFunc([]string{"edev-7"}, srv.URL, "/dcap", "http://192.0.2.1/notify")
		err := subscribe(0, srv.Client())
		srv.Close()

		req := <-got
		if req.path != "/edev/edev-7/sub" || !strings.Contains(req.body, "http://192.0.2.1/notify") {
			t.Errorf("status %d: request path %q body %q, want /edev/edev-7/sub naming the receiver", tc.status, req.path, req.body)
		}
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("status %d: err = %v, want nil", tc.status, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("status %d: err = %v, want it to contain %q", tc.status, err, tc.wantErr)
		}
	}

	if err := newClientSubscribeFunc(nil, "http://127.0.0.1:1", "/dcap", "http://192.0.2.1/notify")(0, http.DefaultClient); err == nil {
		t.Error("client without an edev ID: err = nil, want an error")
	}
}
