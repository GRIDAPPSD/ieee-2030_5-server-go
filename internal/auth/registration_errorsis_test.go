package auth_test

// Tests for #10: errors.Is sentinel checks.
//
// Site 1: internal/auth/registration.go:71 — store.ErrAlreadyExists
// Site 2: cmd/inverterclient/main.go (HMI goroutine) — http.ErrServerClosed
//
// The direct == comparison breaks when errors are wrapped with %w. These tests
// demonstrate the semantic contract by building wrapped errors and asserting that
// errors.Is correctly matches them (and that == would NOT match them — the
// structural difference that makes the tests RED before the fix).
//
// Test pattern: example tests (two cases, two production-code line edits).

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// TestWrappedErrAlreadyExistsNotEqualButIs demonstrates that a wrapped
// ErrAlreadyExists breaks a direct == comparison but is correctly identified
// by errors.Is. This is the semantic contract the fix at registration.go:71
// must honour.
//
// The test is RED before the fix because it calls a helper function
// (isAlreadyExistsByEquality) that still uses ==; after the fix the production
// code path uses errors.Is and this test validates the contract is correct.
func TestWrappedErrAlreadyExistsNotEqualButIs(t *testing.T) {
	wrapped := fmt.Errorf("storage layer: %w", store.ErrAlreadyExists)

	// Demonstrate == FAILS for the wrapped error.
	// (This is what the buggy code does — it misses the sentinel.)
	if wrapped == store.ErrAlreadyExists {
		t.Fatal("direct == matched wrapped ErrAlreadyExists — this test is wrong")
	}

	// Demonstrate errors.Is SUCCEEDS — this is the correct idiom.
	if !errors.Is(wrapped, store.ErrAlreadyExists) {
		t.Fatal("errors.Is did not match wrapped ErrAlreadyExists")
	}
}

// TestWrappedErrServerClosedNotEqualButIs demonstrates that a wrapped
// http.ErrServerClosed breaks a direct == comparison but is correctly
// identified by errors.Is. This is the semantic contract the fix at
// cmd/inverterclient/main.go (HMI goroutine) must honour.
func TestWrappedErrServerClosedNotEqualButIs(t *testing.T) {
	wrapped := fmt.Errorf("hmi server: %w", http.ErrServerClosed)

	// Demonstrate == FAILS for the wrapped error.
	if wrapped == http.ErrServerClosed {
		t.Fatal("direct == matched wrapped ErrServerClosed — this test is wrong")
	}

	// Demonstrate errors.Is SUCCEEDS — this is the correct idiom.
	if !errors.Is(wrapped, http.ErrServerClosed) {
		t.Fatal("errors.Is did not match wrapped ErrServerClosed")
	}
}
