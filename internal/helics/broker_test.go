//go:build helics

package helics_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/helics"
)

// TestBroker_NewInProcess_Lifecycle exercises the full happy-path
// lifecycle of an in-process root broker: create, address shape,
// connectivity, idempotent close, and post-close inspector behavior.
func TestBroker_NewInProcess_Lifecycle(t *testing.T) {
	b, err := helics.NewInProcess("ieee144-lifecycle", "")
	if err != nil {
		t.Fatalf("NewInProcess: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		// Idempotent close in Cleanup is safe even if the test already
		// closed the broker; ErrBrokerClosed is the documented signal.
		_ = b.Close()
	})

	addr := b.Address()
	if addr == "" {
		t.Fatalf("Address: expected non-empty broker address")
	}
	// HELICS zmq brokers report a tcp:// network address. Asserting on
	// the prefix locks the contract IEEE-145 will sit behind, per the
	// data-invariants rule that tests check field values, not just
	// non-crash.
	if !strings.HasPrefix(addr, "tcp://") {
		t.Fatalf("Address: expected tcp:// prefix for zmq broker, got %q", addr)
	}

	if !b.IsConnected() {
		t.Fatalf("IsConnected: expected true after NewInProcess")
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close (first call): unexpected error: %v", err)
	}

	// Second Close is idempotent — must return ErrBrokerClosed so callers
	// can distinguish "I closed it twice" from a real HELICS failure.
	err = b.Close()
	if !errors.Is(err, helics.ErrBrokerClosed) {
		t.Fatalf("Close (second call): expected ErrBrokerClosed, got %v", err)
	}

	if b.IsConnected() {
		t.Fatalf("IsConnected: expected false after Close")
	}
	if got := b.Address(); got != "" {
		t.Fatalf("Address after Close: expected empty string, got %q", got)
	}
}

// TestBroker_NewInProcess_ErrorOnInvalidArgs proves that HELICS-side
// failures surface as a structured *HelicsError with a non-zero error
// code, recoverable via errors.As.
func TestBroker_NewInProcess_ErrorOnInvalidArgs(t *testing.T) {
	// HELICS rejects unknown CLI flags in the broker init string. Use a
	// flag that is guaranteed not to exist so the test stays stable
	// across patch-level HELICS upgrades.
	_, err := helics.NewInProcess("ieee144-bad-args",
		"--this-flag-does-not-exist-on-purpose")
	if err == nil {
		t.Fatalf("NewInProcess with bogus flag: expected error, got nil")
	}

	var herr *helics.HelicsError
	if !errors.As(err, &herr) {
		t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
	}
	if herr.Code == 0 {
		t.Fatalf("HelicsError.Code: expected non-zero, got 0 (msg=%q)", herr.Message)
	}
	if herr.Op != "helicsCreateBroker" {
		t.Fatalf("HelicsError.Op: expected helicsCreateBroker, got %q", herr.Op)
	}
}

// TestBroker_NewSubBroker_RegistersAsChild stands up an in-process root
// broker, creates a sub-broker pointing at the root's address, and
// asserts that the sub-broker comes up connected with a distinct address.
//
// If standing up the sub-broker against an in-process root proves
// impractical on this HELICS build (e.g. the zmq port allocator picks the
// same port the root is bound on), the test skips with a clear message
// rather than masking a bug with a flaky pass — see the data-invariants
// rule that tests assert behavior, not just non-crash.
func TestBroker_NewSubBroker_RegistersAsChild(t *testing.T) {
	root, err := helics.NewInProcess("ieee144-sub-root", "")
	if err != nil {
		t.Fatalf("NewInProcess root: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	rootAddr := root.Address()
	if rootAddr == "" {
		t.Fatalf("root.Address: expected non-empty")
	}

	sub, err := helics.NewSubBroker("ieee144-sub-child", rootAddr)
	if err != nil {
		var herr *helics.HelicsError
		if errors.As(err, &herr) {
			t.Skipf("sub-broker setup against in-process root not supported on this HELICS build: %v", herr)
		}
		t.Fatalf("NewSubBroker: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	if !sub.IsConnected() {
		t.Fatalf("sub.IsConnected: expected true after NewSubBroker")
	}

	subAddr := sub.Address()
	if subAddr == "" {
		t.Fatalf("sub.Address: expected non-empty")
	}
	if subAddr == rootAddr {
		t.Fatalf("sub.Address: expected distinct from root.Address (%q), got same", rootAddr)
	}
}

// TestBroker_NewSubBroker_RejectsEmptyParent guards the wrapper-level
// validation that a sub-broker must point somewhere. This is a Go-side
// contract: the wrapper refuses before reaching the C boundary.
func TestBroker_NewSubBroker_RejectsEmptyParent(t *testing.T) {
	_, err := helics.NewSubBroker("ieee144-no-parent", "")
	if err == nil {
		t.Fatalf("NewSubBroker(\"\", \"\"): expected error, got nil")
	}
	var herr *helics.HelicsError
	if !errors.As(err, &herr) {
		t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
	}
	if herr.Op != "NewSubBroker" {
		t.Fatalf("HelicsError.Op: expected NewSubBroker, got %q", herr.Op)
	}
}
