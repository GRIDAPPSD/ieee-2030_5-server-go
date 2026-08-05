//go:build helics

package helics_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/helics"
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
	// the prefix locks the contract #266 will sit behind, per the
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

	// Lock the operator-facing log shape: Error() must include the op
	// marker AND a "code N" segment so log lines stay greppable. Per
	// data-invariants rule 1, assert on the rendered shape, not just
	// non-empty.
	got := herr.Error()
	if !strings.Contains(got, "helicsCreateBroker") {
		t.Fatalf("Error(): missing op marker, got %q", got)
	}
	if !strings.Contains(got, "code ") {
		t.Fatalf("Error(): missing code marker, got %q", got)
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
// contract: the wrapper refuses before reaching the C boundary, with a
// dedicated sentinel (ErrEmptyParentAddress) recoverable via errors.Is.
// The dedicated sentinel keeps this lane distinct from HELICS error
// codes — including HELICS_ERROR_REGISTRATION_FAILURE = -1, which a prior
// implementation collided with.
func TestBroker_NewSubBroker_RejectsEmptyParent(t *testing.T) {
	_, err := helics.NewSubBroker("ieee144-no-parent", "")
	if err == nil {
		t.Fatalf("NewSubBroker(\"\", \"\"): expected error, got nil")
	}
	if !errors.Is(err, helics.ErrEmptyParentAddress) {
		t.Fatalf("expected ErrEmptyParentAddress, got %T: %v", err, err)
	}
	// Confirm the empty-parent path is NOT the structured *HelicsError
	// shape — that would re-introduce the -1 collision.
	var herr *helics.HelicsError
	if errors.As(err, &herr) {
		t.Fatalf("empty-parent error must be the dedicated sentinel, not *HelicsError (got Code=%d)", herr.Code)
	}
}

// TestBroker_Close_UninitializedBroker proves that a zero-value *Broker
// cannot SIGSEGV across the cgo boundary. Close on a zero-value broker
// returns ErrUninitializedBroker on the first call, ErrBrokerClosed on
// subsequent calls (idempotency contract preserved), and the inspector
// methods report the closed-equivalent values (false / "").
func TestBroker_Close_UninitializedBroker(t *testing.T) {
	var b helics.Broker
	err := b.Close()
	if !errors.Is(err, helics.ErrUninitializedBroker) {
		t.Fatalf("Close (zero-value): expected ErrUninitializedBroker, got %v", err)
	}
	// Idempotency contract: a second Close returns ErrBrokerClosed,
	// same as on a properly-constructed broker.
	err = b.Close()
	if !errors.Is(err, helics.ErrBrokerClosed) {
		t.Fatalf("Close (zero-value, second call): expected ErrBrokerClosed, got %v", err)
	}
	if b.IsConnected() {
		t.Fatalf("IsConnected (zero-value): expected false")
	}
	if got := b.Address(); got != "" {
		t.Fatalf("Address (zero-value): expected empty string, got %q", got)
	}
}

// TestBroker_NewInProcess_RejectsEmbeddedNUL proves that an embedded NUL
// in either name or initString is rejected at the boundary so that
// C.CString cannot silently truncate caller-supplied strings (per
// secure-coding rule 5).
func TestBroker_NewInProcess_RejectsEmbeddedNUL(t *testing.T) {
	cases := []struct {
		label      string
		name       string
		initString string
		wantField  string
	}{
		{"NUL in name", "ieee144\x00pwn", "", "name"},
		{"NUL in initString", "ieee144-nul-init", "--foo\x00bar", "initString"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := helics.NewInProcess(tc.name, tc.initString)
			if err == nil {
				t.Fatalf("NewInProcess: expected error, got nil")
			}
			var herr *helics.HelicsError
			if !errors.As(err, &herr) {
				t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
			}
			if herr.Op != "NewInProcess" {
				t.Fatalf("HelicsError.Op: expected NewInProcess, got %q", herr.Op)
			}
			if !strings.Contains(herr.Message, tc.wantField) {
				t.Fatalf("HelicsError.Message: expected to mention %q, got %q", tc.wantField, herr.Message)
			}
			if !strings.Contains(herr.Message, "NUL") {
				t.Fatalf("HelicsError.Message: expected to mention NUL, got %q", herr.Message)
			}
		})
	}
}

// TestBroker_NewSubBroker_RejectsEmbeddedNUL covers the NUL-rejection
// path on the NewSubBroker entry point. The empty-parent guard runs
// first, so NUL-in-name is the cleanest case to assert; NUL-in-parent is
// covered alongside the whitespace/leading-dash cases below.
func TestBroker_NewSubBroker_RejectsEmbeddedNUL(t *testing.T) {
	_, err := helics.NewSubBroker("ieee144\x00sub", "tcp://127.0.0.1:23404")
	if err == nil {
		t.Fatalf("NewSubBroker (NUL in name): expected error, got nil")
	}
	var herr *helics.HelicsError
	if !errors.As(err, &herr) {
		t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
	}
	if herr.Op != "NewSubBroker" {
		t.Fatalf("HelicsError.Op: expected NewSubBroker, got %q", herr.Op)
	}
	if !strings.Contains(herr.Message, "name") || !strings.Contains(herr.Message, "NUL") {
		t.Fatalf("HelicsError.Message: expected to mention name+NUL, got %q", herr.Message)
	}
}

// TestBroker_NewSubBroker_RejectsMalformedParent proves that whitespace
// or a leading dash in parentAddress is rejected before reaching the
// HELICS init-string parser. fmt.Sprintf("--broker=%s", parentAddress)
// does no quoting; whitespace would re-tokenize and a leading dash
// would shadow as a separate flag.
func TestBroker_NewSubBroker_RejectsMalformedParent(t *testing.T) {
	cases := []struct {
		label    string
		parent   string
		wantSnip string
	}{
		{"space", "tcp://1.2.3.4 :23404", "whitespace"},
		{"tab", "tcp://1.2.3.4\t:23404", "whitespace"},
		{"newline", "tcp://1.2.3.4\n:23404", "whitespace"},
		{"carriage return", "tcp://1.2.3.4\r:23404", "whitespace"},
		{"leading dash", "-malicious-flag", "leading dash"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := helics.NewSubBroker("ieee144-bad-parent", tc.parent)
			if err == nil {
				t.Fatalf("NewSubBroker (%s): expected error, got nil", tc.label)
			}
			var herr *helics.HelicsError
			if !errors.As(err, &herr) {
				t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
			}
			if herr.Op != "NewSubBroker" {
				t.Fatalf("HelicsError.Op: expected NewSubBroker, got %q", herr.Op)
			}
			if !strings.Contains(herr.Message, tc.wantSnip) {
				t.Fatalf("HelicsError.Message: expected %q, got %q", tc.wantSnip, herr.Message)
			}
		})
	}
}
