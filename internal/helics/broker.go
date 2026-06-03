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
	"sync"
	"unsafe"
)

// defaultBrokerType is the HELICS core type used when callers don't
// override. HELICS v3's conventional default is "zmq" (used by the
// helics_broker CLI and the v3 docs); it is also the transport that the
// in-process unit test exercises without external setup.
const defaultBrokerType = "zmq"

// ErrBrokerClosed is returned by Broker.Close on the second and subsequent
// calls so that idempotent teardown is verifiable via errors.Is.
var ErrBrokerClosed = errors.New("helics: broker already closed")

// HelicsError captures a non-zero HELICS C error code plus its message,
// translated across the cgo boundary into a Go-native error. Callers can
// recover the structured form with errors.As.
type HelicsError struct {
	// Op is the wrapper-level operation that triggered the error
	// (e.g. "helicsCreateBroker"). It anchors the error to the C entry
	// point that produced it.
	Op string
	// Code is the HELICS error_code field (matches HelicsErrorTypes in
	// helics_enums.h; non-zero means error).
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
func NewInProcess(name, initString string) (*Broker, error) {
	return createBroker(defaultBrokerType, name, initString)
}

// NewSubBroker creates a local broker that registers as a child of an
// existing root broker reachable at parentAddress. It maps to the HELICS 3
// sub-broker pattern: the same helicsCreateBroker entry point with an
// init string of "--broker=<parentAddress>". v3.6.1 has no broker-side
// Connect entry point (see ADR-004's 2026-06-03 correction).
func NewSubBroker(name, parentAddress string) (*Broker, error) {
	if parentAddress == "" {
		return nil, &HelicsError{
			Op:      "NewSubBroker",
			Code:    -1,
			Message: "parentAddress is required",
		}
	}
	initString := fmt.Sprintf("--broker=%s", parentAddress)
	return createBroker(defaultBrokerType, name, initString)
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
			Code:    -1,
			Message: "HELICS returned nil handle with no error code",
		}
	}

	return &Broker{handle: handle, name: name}, nil
}

// Address returns the broker's network address (e.g. "tcp://127.0.0.1:23404"
// for zmq). Returns an empty string if the broker has been closed; callers
// that need to distinguish should check IsConnected first.
//
// The returned string is copied into Go memory; HELICS retains ownership
// of the underlying C buffer.
func (b *Broker) Address() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.handle == nil {
		return ""
	}
	cStr := C.helicsBrokerGetAddress(b.handle)
	// HELICS owns cStr; do NOT free it. C.GoString copies it into a
	// Go-managed string.
	return C.GoString(cStr)
}

// IsConnected reports whether the broker considers itself connected. It
// returns false after Close.
func (b *Broker) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.handle == nil {
		return false
	}
	return C.helicsBrokerIsConnected(b.handle) == C.HELICS_TRUE
}

// Close disconnects and frees the underlying HELICS broker. It is
// idempotent: the first call performs the teardown and returns any
// HELICS-side disconnect error; subsequent calls return ErrBrokerClosed
// (recoverable via errors.Is).
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBrokerClosed
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
