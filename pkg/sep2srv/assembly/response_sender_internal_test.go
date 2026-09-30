package assembly

import "testing"

// With no identity source there is no sender to check, so no authorizer is
// built and the Response POST refuses every Response naming a device.
func TestResponseSenderAuthorizerWithoutIdentityIsNil(t *testing.T) {
	if got := responseSenderAuthorizer(nil, nil); got != nil {
		t.Fatal("responseSenderAuthorizer(nil, ...) returned an authorizer, want nil")
	}
}
