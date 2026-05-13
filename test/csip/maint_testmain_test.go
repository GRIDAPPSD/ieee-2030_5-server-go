//go:build csip_test_hooks

// TestMain for the csip_test_hooks-tagged test surface.
//
// Sets SEP2_TEST_MUTATION_TOKEN at process start so every BootServer
// call across the MAINT-* test suite registers the /test/mutations/
// surface deterministically. Using a TestMain (rather than t.Setenv per
// test) lets the MAINT-* tests run with t.Parallel() — t.Setenv and
// t.Parallel are mutually exclusive in stdlib testing as of Go 1.17.
//
// Only compiled under -tags csip_test_hooks. Untagged builds have no
// TestMain in this package and fall back to the stdlib default.
//
// IEEE-091 / Phase 6.

package csip_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Set the mutation-surface auth token before any test boots a
	// server. BootServer reads the env at construction time; tests
	// that need /test/mutations/ routes registered must see this set.
	// The value matches maintMutationToken in maint_helpers_test.go.
	if err := os.Setenv(maintMutationTokenEnv, maintMutationToken); err != nil {
		// os.Setenv on a Unix-y host effectively never fails, but
		// surfacing it here is cheaper than chasing a 404-on-mutation
		// mystery later.
		_, _ = os.Stderr.WriteString("csip test: set " + maintMutationTokenEnv + ": " + err.Error() + "\n")
		os.Exit(2)
	}
	os.Exit(m.Run())
}
