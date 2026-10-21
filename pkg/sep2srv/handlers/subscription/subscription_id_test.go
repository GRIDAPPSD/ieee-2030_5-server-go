package subscription_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/idmint/idminttest"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Not parallel: it holds the process-wide id clock still (#1010).
func TestCreateSubscriptionParallelPostsGetDistinctIDs(t *testing.T) {
	allowURI := func(context.Context, string) error { return nil }
	h := subscription.HandleCreateSubscription(memory.NewSubscriptionStore(), allowURI, allowAnyResource)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/sub", h)
	idminttest.AssertParallelCreatesDistinct(t, mux, 16, func() *http.Request {
		return newSubscriptionRequest(t, context.Background(), "3", "/edev/3/fsa", "http://192.0.2.1/n")
	})
}
