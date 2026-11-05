package assembly

import (
	"testing"
)

// TestManagerOnlyWritesNameOnlyMountedPatterns fails when a row outlives its
// route, which would read as live policy while granting nothing.
func TestManagerOnlyWritesNameOnlyMountedPatterns(t *testing.T) {
	t.Parallel()
	stores := fullStores()
	policy := AuthPolicy{Identity: ownerIdentity("OWNER")}
	_, patterns := BuildProtocolRouter(RouterConfig{}, stores, policy, "serverSFDI", "serverLFDI", nil)

	mounted := map[string]bool{}
	for _, p := range patterns {
		mounted[p] = true
	}
	if len(managerOnlyWrites) == 0 {
		t.Fatal("managerOnlyWrites is empty; the stale-row check would pass vacuously")
	}
	if !mounted["GET /mup"] {
		t.Fatal("control: the router did not report GET /mup, so the pattern list cannot be trusted")
	}
	for p := range managerOnlyWrites {
		if !mounted[p] {
			t.Errorf("managerOnlyWrites names %q, which is not a currently mounted pattern; remove the stale row", p)
		}
	}
}
