//go:build helics

// Package helics is an in-house cgo wrapper over the HELICS v3 C API
// (libhelics, system-installed at /usr/local). It is built only under the
// `helics` build tag so that the default `go build` of the sep2 server
// stays CGO-free (per ADR-002 / ADR-004 in the Knowledge workspace).
//
// The wrapper deliberately exposes only the broker-side surface IEEE-144
// needs: an in-process root broker, a sub-broker that registers as a child
// of an external root, address/connectivity inspection, and idempotent
// teardown. Federate primitives belong to IEEE-145.
package helics

/*
#cgo CFLAGS: -I/usr/local/include
#cgo LDFLAGS: -L/usr/local/lib -lhelics
#include <stdlib.h>
#include <helics/helics.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unsafe"
)

// defaultBrokerType is the HELICS core type used when callers don't
// override. HELICS v3's conventional default is "zmq" (used by the
// helics_broker CLI and the v3 docs); it is also the transport that the
// in-process unit test exercises without external setup.
const defaultBrokerType = "zmq"

// helicsAnomalousCode marks a failure that crossed the C boundary but for
// which HELICS did not populate an error code, as well as wrapper-side
// boundary-input rejections that we surface in *HelicsError shape for
// consistency with the cgo-boundary lane. Distinct from any
// HelicsErrorTypes value (HELICS error codes occupy -8..-1 and small
// positive sentinels; math.MinInt32 is well outside that range).
const helicsAnomalousCode = math.MinInt32

// ErrBrokerClosed is returned by Broker.Close on the second and subsequent
// calls so that idempotent teardown is verifiable via errors.Is.
var ErrBrokerClosed = errors.New("helics: broker already closed")

// ErrEmptyParentAddress is returned by NewSubBroker when the caller passes
// an empty parentAddress. Recoverable via errors.Is. Distinct from any
// *HelicsError because the failure is wrapper-side validation that never
// reached the C boundary.
var ErrEmptyParentAddress = errors.New("helics: parentAddress is required")

// ErrUninitializedBroker is returned by Broker methods when invoked on a
// zero-value *Broker (one that did not come from NewInProcess or
// NewSubBroker). Without this guard the C calls would dereference a nil
// HelicsBroker handle and SIGSEGV across the cgo boundary.
var ErrUninitializedBroker = errors.New("helics: broker not initialized via NewInProcess or NewSubBroker")

// HelicsError captures a non-zero HELICS C error code plus its message,
// translated across the cgo boundary into a Go-native error. Callers can
// recover the structured form with errors.As.
type HelicsError struct {
	// Op is the wrapper-level operation that triggered the error
	// (e.g. "helicsCreateBroker"). It anchors the error to the C entry
	// point that produced it.
	Op string
	// Code is the HELICS error_code field (matches HelicsErrorTypes in
	// helics_enums.h) when the failure originated from a C call. For
	// wrapper-side anomalies that crossed the C boundary without a
	// populated HELICS code, see helicsAnomalousCode.
	Code int32
	// Message is the HELICS-side message string, copied into Go memory.
	Message string
}

// Error renders the HELICS error in the conventional "op: code N: message"
// shape so log lines stay greppable.
func (e *HelicsError) Error() string {
	return fmt.Sprintf("helics: %s: code %d: %s", e.Op, e.Code, e.Message)
}

// Broker wraps a HelicsBroker handle and tracks lifecycle state so that
// Close is idempotent and post-close inspectors don't dereference a freed
// handle.
type Broker struct {
	mu     sync.Mutex
	handle C.HelicsBroker
	name   string
	closed bool
}

// NewInProcess creates a root broker that runs in-process inside the
// caller's process via helicsCreateBroker. It is suitable for unit tests
// and for the server-owned root broker shape described in ADR-004.
//
// initString is passed verbatim to HELICS; pass an empty string to accept
// the v3 defaults (the in-process tests rely on this).
//
// Embedded NUL bytes in name or initString are rejected at the boundary
// before crossing into C: C.CString silently truncates at the first NUL,
// which would let a caller-supplied "sub\x00malicious" register as "sub"
// (see secure-coding rule 5).
func NewInProcess(name, initString string) (*Broker, error) {
	if err := rejectNUL("NewInProcess", "name", name); err != nil {
		return nil, err
	}
	if err := rejectNUL("NewInProcess", "initString", initString); err != nil {
		return nil, err
	}
	return createBroker(defaultBrokerType, name, initString)
}

// NewSubBroker creates a local broker that registers as a child of an
// existing root broker reachable at parentAddress. It maps to the HELICS 3
// sub-broker pattern: the same helicsCreateBroker entry point with an
// init string of "--broker=<parentAddress>". v3.6.1 has no broker-side
// Connect entry point (see ADR-004's 2026-06-03 correction).
//
// An empty parentAddress is wrapper-side validation and returns
// ErrEmptyParentAddress (recoverable via errors.Is). Embedded NUL bytes,
// ASCII whitespace, or a leading dash in parentAddress are rejected
// because the constructed init string "--broker=<parentAddress>" feeds a
// CLI-style argument parser on the HELICS side; whitespace would
// re-tokenize and a leading dash would shadow as a separate flag.
func NewSubBroker(name, parentAddress string) (*Broker, error) {
	if parentAddress == "" {
		return nil, ErrEmptyParentAddress
	}
	if err := rejectNUL("NewSubBroker", "name", name); err != nil {
		return nil, err
	}
	if err := rejectNUL("NewSubBroker", "parentAddress", parentAddress); err != nil {
		return nil, err
	}
	if strings.ContainsAny(parentAddress, " \t\n\r") {
		return nil, &HelicsError{
			Op:      "NewSubBroker",
			Code:    helicsAnomalousCode,
			Message: "parentAddress: contains whitespace",
		}
	}
	if strings.HasPrefix(parentAddress, "-") {
		return nil, &HelicsError{
			Op:      "NewSubBroker",
			Code:    helicsAnomalousCode,
			Message: "parentAddress: leading dash forbidden",
		}
	}
	initString := fmt.Sprintf("--broker=%s", parentAddress)
	return createBroker(defaultBrokerType, name, initString)
}

// rejectNUL returns a *HelicsError when s contains an embedded NUL byte.
// We use the structured-error shape with helicsAnomalousCode so callers
// in the cgo-boundary lane can errors.As uniformly; the dedicated sentinel
// code keeps it distinct from any genuine HelicsErrorTypes value.
func rejectNUL(op, field, s string) error {
	if strings.IndexByte(s, 0) != -1 {
		return &HelicsError{
			Op:      op,
			Code:    helicsAnomalousCode,
			Message: fmt.Sprintf("%s: contains NUL byte", field),
		}
	}
	return nil
}

// createBroker is the shared cgo entry point for both shapes. It owns the
// C-string lifetimes and the HelicsError translation.
func createBroker(brokerType, name, initString string) (*Broker, error) {
	cType := C.CString(brokerType)
	defer C.free(unsafe.Pointer(cType))

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	cInit := C.CString(initString)
	defer C.free(unsafe.Pointer(cInit))

	cErr := C.helicsErrorInitialize()
	handle := C.helicsCreateBroker(cType, cName, cInit, &cErr)

	if cErr.error_code != 0 {
		// HELICS allocates the message string; copy it into Go memory
		// before clearing the error to avoid dangling pointers.
		msg := C.GoString(cErr.message)
		code := int32(cErr.error_code)
		C.helicsErrorClear(&cErr)
		// If HELICS returned a handle alongside the error, free it so we
		// don't leak. The C API documents this as "may return null on
		// error", but defensive cleanup is cheap.
		if handle != nil {
			C.helicsBrokerFree(handle)
		}
		return nil, &HelicsError{
			Op:      "helicsCreateBroker",
			Code:    code,
			Message: msg,
		}
	}

	if handle == nil {
		return nil, &HelicsError{
			Op:      "helicsCreateBroker",
			Code:    helicsAnomalousCode,
			Message: "HELICS returned nil handle with no error code",
		}
	}

	return &Broker{handle: handle, name: name}, nil
}

// Address returns the broker's network address (e.g. "tcp://127.0.0.1:23404"
// for zmq). Returns an empty string if the broker has been closed or is
// uninitialized (zero-value *Broker); callers that need to distinguish
// should check IsConnected first.
//
// The returned string is copied into Go memory; HELICS retains ownership
// of the underlying C buffer.
func (b *Broker) Address() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.handle == nil {
		// Zero-value or post-close: never reach the C call. A nil
		// HelicsBroker handle would SIGSEGV across cgo.
		return ""
	}
	cStr := C.helicsBrokerGetAddress(b.handle)
	// HELICS owns cStr; do NOT free it. C.GoString copies it into a
	// Go-managed string.
	return C.GoString(cStr)
}

// IsConnected reports whether the broker considers itself connected. It
// returns false after Close and on a zero-value *Broker.
func (b *Broker) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.handle == nil {
		// Zero-value or post-close: never reach the C call.
		return false
	}
	return C.helicsBrokerIsConnected(b.handle) == C.HELICS_TRUE
}

// Close disconnects and frees the underlying HELICS broker. It is
// idempotent: the first call performs the teardown and returns any
// HELICS-side disconnect error; subsequent calls return ErrBrokerClosed
// (recoverable via errors.Is).
//
// On a zero-value *Broker (one not produced by NewInProcess or
// NewSubBroker), Close marks the broker closed and returns
// ErrUninitializedBroker without touching the cgo boundary; subsequent
// calls then return ErrBrokerClosed per the idempotency contract.
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBrokerClosed
	}
	if b.handle == nil {
		// Zero-value *Broker: never constructed. Mark closed so the
		// idempotency contract still holds, and refuse without making
		// any C call against a nil HelicsBroker.
		b.closed = true
		return ErrUninitializedBroker
	}
	b.closed = true

	// Best-effort: call Disconnect first, then Free regardless of the
	// disconnect outcome. Free has no err out-param, so any failure is
	// confined to the disconnect step.
	cErr := C.helicsErrorInitialize()
	C.helicsBrokerDisconnect(b.handle, &cErr)
	var disconnectErr error
	if cErr.error_code != 0 {
		disconnectErr = &HelicsError{
			Op:      "helicsBrokerDisconnect",
			Code:    int32(cErr.error_code),
			Message: C.GoString(cErr.message),
		}
		C.helicsErrorClear(&cErr)
	}

	C.helicsBrokerFree(b.handle)
	b.handle = nil

	if disconnectErr != nil {
		return fmt.Errorf("helics close: %w", disconnectErr)
	}
	return nil
}
