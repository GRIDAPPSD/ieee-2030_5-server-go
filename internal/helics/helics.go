//go:build helics

// Package helics is an in-house cgo wrapper over the HELICS v3 C API
// (libhelics, system-installed; resolved via pkg-config). It is built
// only under the `helics` build tag so that the default `go build` of
// the sep2 server stays CGO-free (per ADR-002 / ADR-004).
//
// # File layout: single cgo translation unit
//
// All cgo-touching code in this package lives in this single file. The
// HELICS C header (`helics/helics.h`) declares several externally-linked
// `const` values with initializers at file scope (e.g. HELICS_TRUE,
// HELICS_FALSE, HELICS_TIME_ZERO, HELICS_INVALID_OPTION_INDEX,
// HELICS_INVALID_PROPERTY_VALUE, cHelicsBigNumber, etc.; see
// helics.h:297, 345, 463, 557-560, 569-570). Each cgo translation unit
// that includes the header emits its own definition of those symbols,
// so as soon as two cgo `.go` files in the same Go package both
// `#include <helics/helics.h>` the package-level link fails with
// "multiple definition".
//
// The portable resolution is to keep all `import "C"` (and therefore all
// C calls) in one Go file. A GNU-ld linker workaround (the
// allow-duplicate-definition flag) was considered and rejected because
// that flag is GNU-ld specific and is silently missing from macOS's
// ld64; ADR-004's second 2026-06-03 correction locked the wrapper to
// portable-prefix builds (Homebrew on macOS, Spack/Easybuild, distro
// packages), so a GNU-ld-only flag would break the very portability
// that ADR enshrined. We also considered an extern-shim .c file, but
// `helics.h`, `helics_api.h`, and `helics_enums.h` all carry the same
// file-scope const definitions, so no header subset avoids the
// duplicates.
//
// The file is therefore organized by exported type (Broker first, then
// Federate), with shared error sentinels and HelicsError at the top.
// Tests live in the external `helics_test` package and exercise only
// the exported API.
package helics

/*
#cgo pkg-config: helics
#include <stdlib.h>
#include <helics/helics.h>
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// -----------------------------------------------------------------------------
// Shared package-wide types and sentinels
// -----------------------------------------------------------------------------

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

// consumeHelicsError copies the HELICS error message into Go memory,
// clears the C-side struct (so subsequent reuse of cErr is clean), and
// returns the structured Go-side error. Centralizing this avoids the
// "remember to copy then clear" footgun on every call site.
func consumeHelicsError(op string, cErr *C.HelicsError) error {
	msg := C.GoString(cErr.message)
	code := int32(cErr.error_code)
	C.helicsErrorClear(cErr)
	return &HelicsError{Op: op, Code: code, Message: msg}
}

// -----------------------------------------------------------------------------
// Broker (#265)
// -----------------------------------------------------------------------------

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
		// If HELICS returned a handle alongside the error, free it so we
		// don't leak. The C API documents this as "may return null on
		// error", but defensive cleanup is cheap. Free BEFORE consuming
		// the error since consumeHelicsError clears cErr.
		if handle != nil {
			C.helicsBrokerFree(handle)
		}
		return nil, consumeHelicsError("helicsCreateBroker", &cErr)
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
		disconnectErr = consumeHelicsError("helicsBrokerDisconnect", &cErr)
	}

	C.helicsBrokerFree(b.handle)
	b.handle = nil

	if disconnectErr != nil {
		return fmt.Errorf("helics close: %w", disconnectErr)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Federate (#266)
// -----------------------------------------------------------------------------

// ErrFederateClosed is returned by Federate.Close on the second and
// subsequent calls so that idempotent teardown is verifiable via
// errors.Is. Mirrors ErrBrokerClosed on the broker side.
var ErrFederateClosed = errors.New("helics: federate already closed")

// ErrUninitializedFederate is returned by Federate methods invoked on a
// zero-value *Federate (one that did not come from NewFederate).
// Without this guard the cgo calls would dereference a nil
// HelicsFederate handle and SIGSEGV across the cgo boundary. Mirrors
// ErrUninitializedBroker on the broker side.
var ErrUninitializedFederate = errors.New("helics: federate not initialized via NewFederate")

// ErrEmptyTopic is returned when a publish/subscribe topic argument is
// empty. Wrapper-side validation; never reaches the C boundary.
var ErrEmptyTopic = errors.New("helics: topic is required")

// Value carries a single HELICS value-federate update across the
// Subscribe channel. The Topic field echoes the registered subscription
// key so consumers that subscribe to multiple topics on the same
// federate can route by string. The Time field carries the granted
// HELICS time the update was observed.
type Value struct {
	Topic string
	Value float64
	Time  time.Duration
}

// FederateConfig configures a value federate. BrokerAddress=="" spins
// up an in-process broker that the federate owns for its lifetime;
// non-empty targets a separate helics_broker daemon (or any reachable
// broker) at that address. StepSize is the federate's TIME_PERIOD; it
// also bounds the granted time returned from Step.
type FederateConfig struct {
	Name          string
	BrokerAddress string
	StepSize      time.Duration
}

// Federate wraps a HelicsValueFederate handle plus the goroutine and
// channel state for the Subscribe surface. Lifecycle:
//
//	NewFederate -> RegisterPublication / Subscribe (initializing) ->
//	EnterExecutingMode -> Publish / Step (executing) -> Close.
//
// All methods are safe to call on a zero-value *Federate; they return
// ErrUninitializedFederate. Close is idempotent: the first call tears
// down the cgo handle, the goroutine, and any owned broker; subsequent
// calls return ErrFederateClosed.
type Federate struct {
	mu     sync.Mutex
	handle C.HelicsFederate
	closed bool

	// publications and subscriptions keep cgo handle ownership tied to
	// the federate so the user never sees a HelicsPublication or
	// HelicsInput. Both are freed implicitly by helicsFederateFree.
	publications  map[string]C.HelicsPublication
	subscriptions map[string]subscription

	// ownedBroker, when non-nil, is an in-process broker created for
	// FederateConfig.BrokerAddress=="". The federate Closes it as part
	// of its own teardown so the caller does not have to.
	ownedBroker *Broker

	// pumpDone signals the subscription pump goroutine to exit. Closed
	// by Close once. The pump exits on close(pumpDone) and is joined
	// via pumpWG before any cgo handle is freed.
	pumpDone chan struct{}
	pumpWG   sync.WaitGroup
	// inFlight tracks Step calls that have released f.mu but are still
	// inside a blocking cgo call against the handle. Close drains it
	// before freeing the handle so a stuck peer in helicsFederateRequestTime
	// cannot wedge teardown via the mutex AND a freed handle cannot be
	// dereferenced by a still-running Step. New Step calls are gated by
	// the f.closed check under f.mu, so once Close marks closed nothing
	// else can increment inFlight.
	inFlight sync.WaitGroup
	// pumpTrigger fires once per Step so the pump only checks for
	// updates when the federate has advanced time. Buffer of 1 with
	// drop-on-full keeps Step non-blocking; the pump catches up on its
	// next wakeup.
	pumpTrigger chan struct{}
}

// subscription bundles the cgo handle with the channel the pump
// goroutine writes to and the topic key (echoed back to consumers).
type subscription struct {
	input C.HelicsInput
	ch    chan Value
	topic string
}

// NewFederate constructs and connects a value federate. It wraps
// helicsCreateValueFederate plus the FederateInfo setup needed to point
// at the chosen broker. The federate is in the HELICS "initializing"
// state when this function returns; register publications and
// subscriptions before calling EnterExecutingMode.
//
// When cfg.BrokerAddress == "", the federate creates and owns an
// in-process root broker via NewInProcess; the federate Closes that
// broker as part of its own teardown.
//
// ctx is honored for cancellation only up to the cgo boundary: the
// HELICS C calls are blocking and not interruptible from Go. If ctx is
// already cancelled, NewFederate returns ctx.Err() before constructing
// any C state.
func NewFederate(ctx context.Context, cfg FederateConfig) (*Federate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := rejectNUL("NewFederate", "name", cfg.Name); err != nil {
		return nil, err
	}
	if cfg.BrokerAddress != "" {
		if err := validateBrokerAddress("NewFederate", cfg.BrokerAddress); err != nil {
			return nil, err
		}
	}

	var ownedBroker *Broker
	address := cfg.BrokerAddress
	if address == "" {
		// Caller wants a self-contained federation. Spin up a root
		// in-process broker we own and tear down on Close.
		b, err := NewInProcess(cfg.Name+"-broker", "")
		if err != nil {
			return nil, fmt.Errorf("helics NewFederate: own broker: %w", err)
		}
		ownedBroker = b
		address = b.Address()
	}

	handle, err := createValueFederate(cfg.Name, address, cfg.StepSize)
	if err != nil {
		if ownedBroker != nil {
			_ = ownedBroker.Close()
		}
		return nil, err
	}

	f := &Federate{
		handle:        handle,
		publications:  make(map[string]C.HelicsPublication),
		subscriptions: make(map[string]subscription),
		ownedBroker:   ownedBroker,
		pumpDone:      make(chan struct{}),
		pumpTrigger:   make(chan struct{}, 1),
	}
	f.pumpWG.Add(1)
	go f.pumpUpdates()
	return f, nil
}

// validateBrokerAddress runs the same CLI-injection guards
// Broker.NewSubBroker uses, since the federate's HELICS init string is
// constructed verbatim from cfg.BrokerAddress (no quoting on the
// HELICS side). Rejects the full ASCII whitespace class
// (space/tab/CR/LF/VT/FF) so a CLI-style argument parser on the HELICS
// side cannot re-tokenize the init string. CLI11/boost isspace(3) treats
// \v and \f as whitespace too, so omitting them would be a
// re-tokenization bypass (see secure-coding rule 1+5).
func validateBrokerAddress(op, addr string) error {
	if err := rejectNUL(op, "BrokerAddress", addr); err != nil {
		return err
	}
	if strings.ContainsAny(addr, " \t\n\r\v\f") {
		return &HelicsError{
			Op:      op,
			Code:    helicsAnomalousCode,
			Message: "BrokerAddress: contains whitespace",
		}
	}
	if strings.HasPrefix(addr, "-") {
		return &HelicsError{
			Op:      op,
			Code:    helicsAnomalousCode,
			Message: "BrokerAddress: leading dash forbidden",
		}
	}
	return nil
}

// createValueFederate is the shared cgo entry point for federate
// construction. It owns the FederateInfo lifetime, every C-string
// allocation, and the HelicsError translation.
func createValueFederate(name, address string, stepSize time.Duration) (C.HelicsFederate, error) {
	info := C.helicsCreateFederateInfo()
	if info == nil {
		return nil, &HelicsError{
			Op:      "helicsCreateFederateInfo",
			Code:    helicsAnomalousCode,
			Message: "HELICS returned nil HelicsFederateInfo",
		}
	}
	defer C.helicsFederateInfoFree(info)

	cErr := C.helicsErrorInitialize()

	// Use the same default core type the broker uses: zmq.
	C.helicsFederateInfoSetCoreType(info, C.int(C.HELICS_CORE_TYPE_ZMQ), &cErr)
	if cErr.error_code != 0 {
		return nil, consumeHelicsError("helicsFederateInfoSetCoreType", &cErr)
	}

	// --broker=<address> is the canonical HELICS-3 init string that
	// points the federate's local core at an existing root broker. The
	// validation above guarantees no whitespace and no leading dash so
	// the HELICS CLI parser cannot re-tokenize it.
	initString := fmt.Sprintf("--broker=%s", address)
	cInit := C.CString(initString)
	defer C.free(unsafe.Pointer(cInit))
	C.helicsFederateInfoSetCoreInitString(info, cInit, &cErr)
	if cErr.error_code != 0 {
		return nil, consumeHelicsError("helicsFederateInfoSetCoreInitString", &cErr)
	}

	if stepSize > 0 {
		C.helicsFederateInfoSetTimeProperty(info,
			C.int(C.HELICS_PROPERTY_TIME_PERIOD),
			C.HelicsTime(stepSize.Seconds()), &cErr)
		if cErr.error_code != 0 {
			return nil, consumeHelicsError("helicsFederateInfoSetTimeProperty", &cErr)
		}
	}

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	handle := C.helicsCreateValueFederate(cName, info, &cErr)
	if cErr.error_code != 0 {
		// HELICS may return a non-nil handle even alongside an error;
		// free it defensively before surfacing the error.
		if handle != nil {
			C.helicsFederateDestroy(handle)
		}
		return nil, consumeHelicsError("helicsCreateValueFederate", &cErr)
	}
	if handle == nil {
		return nil, &HelicsError{
			Op:      "helicsCreateValueFederate",
			Code:    helicsAnomalousCode,
			Message: "HELICS returned nil handle with no error code",
		}
	}
	return handle, nil
}

// RegisterPublication registers a typed double publication on the
// federate. Wraps helicsFederateRegisterPublication with
// HELICS_DATA_TYPE_DOUBLE. Must be called before EnterExecutingMode;
// HELICS rejects pub/sub registration once executing mode is entered.
func (f *Federate) RegisterPublication(topic, units string) error {
	if topic == "" {
		return ErrEmptyTopic
	}
	if err := rejectNUL("RegisterPublication", "topic", topic); err != nil {
		return err
	}
	if err := rejectNUL("RegisterPublication", "units", units); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// closed first, handle nil second: post-Close paths nil the handle
	// as part of teardown, so the handle-nil check would otherwise mask
	// a closed federate as zero-value.
	if f.closed {
		return ErrFederateClosed
	}
	if f.handle == nil {
		return ErrUninitializedFederate
	}
	if _, exists := f.publications[topic]; exists {
		// Idempotent: re-registering the same topic returns nil rather
		// than a HELICS-side duplicate-key error. Matches Go map
		// idioms and lets callers register defensively.
		return nil
	}

	cTopic := C.CString(topic)
	defer C.free(unsafe.Pointer(cTopic))
	cUnits := C.CString(units)
	defer C.free(unsafe.Pointer(cUnits))

	cErr := C.helicsErrorInitialize()
	pub := C.helicsFederateRegisterPublication(f.handle, cTopic,
		C.HelicsDataTypes(C.HELICS_DATA_TYPE_DOUBLE), cUnits, &cErr)
	if cErr.error_code != 0 {
		return consumeHelicsError("helicsFederateRegisterPublication", &cErr)
	}
	if pub == nil {
		return &HelicsError{
			Op:      "helicsFederateRegisterPublication",
			Code:    helicsAnomalousCode,
			Message: "HELICS returned nil publication with no error code",
		}
	}
	f.publications[topic] = pub
	return nil
}

// Subscribe registers a value subscription on the federate and returns
// a channel the wrapper drains updates into. The channel is owned by
// the federate: the caller must not close it. The channel is buffered
// (latest-value semantics, drop-on-full); when the federate Closes,
// the wrapper closes the channel so consumers can detect shutdown via
// the comma-ok receive.
//
// Subscribe must be called before EnterExecutingMode; HELICS rejects
// pub/sub registration once executing mode is entered.
func (f *Federate) Subscribe(topic string) (<-chan Value, error) {
	if topic == "" {
		return nil, ErrEmptyTopic
	}
	if err := rejectNUL("Subscribe", "topic", topic); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrFederateClosed
	}
	if f.handle == nil {
		return nil, ErrUninitializedFederate
	}
	if existing, ok := f.subscriptions[topic]; ok {
		// Idempotent: hand back the existing channel rather than
		// creating a parallel HELICS-side input.
		return existing.ch, nil
	}

	cTopic := C.CString(topic)
	defer C.free(unsafe.Pointer(cTopic))
	// Empty units: HELICS treats "" as no unit conversion.
	cUnits := C.CString("")
	defer C.free(unsafe.Pointer(cUnits))

	cErr := C.helicsErrorInitialize()
	input := C.helicsFederateRegisterSubscription(f.handle, cTopic, cUnits, &cErr)
	if cErr.error_code != 0 {
		return nil, consumeHelicsError("helicsFederateRegisterSubscription", &cErr)
	}
	if input == nil {
		return nil, &HelicsError{
			Op:      "helicsFederateRegisterSubscription",
			Code:    helicsAnomalousCode,
			Message: "HELICS returned nil input with no error code",
		}
	}

	// Buffered to 1 so a slow consumer cannot wedge the pump goroutine
	// or the Step caller; on full we drop+replace, which is the
	// latest-value semantics HELICS values represent. The drop-and-replace
	// pattern in drainUpdates is race-free ONLY because the pump goroutine
	// is the sole sender on this channel; do not move sends elsewhere
	// without re-thinking the invariant.
	ch := make(chan Value, 1)
	f.subscriptions[topic] = subscription{input: input, ch: ch, topic: topic}
	return ch, nil
}

// EnterExecutingMode wraps helicsFederateEnterExecutingMode. After this
// returns nil the federate is in the executing state and pub/sub
// registration is no longer permitted.
func (f *Federate) EnterExecutingMode() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrFederateClosed
	}
	if f.handle == nil {
		return ErrUninitializedFederate
	}
	cErr := C.helicsErrorInitialize()
	C.helicsFederateEnterExecutingMode(f.handle, &cErr)
	if cErr.error_code != 0 {
		return consumeHelicsError("helicsFederateEnterExecutingMode", &cErr)
	}
	return nil
}

// Publish wraps helicsPublicationPublishDouble for a previously
// registered topic. Returns ErrEmptyTopic on "", ErrUninitializedFederate
// on a zero-value receiver, ErrFederateClosed after Close, and a
// *HelicsError for any HELICS-side failure.
func (f *Federate) Publish(topic string, value float64) error {
	if topic == "" {
		return ErrEmptyTopic
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrFederateClosed
	}
	if f.handle == nil {
		return ErrUninitializedFederate
	}
	pub, ok := f.publications[topic]
	if !ok {
		return &HelicsError{
			Op:      "Publish",
			Code:    helicsAnomalousCode,
			Message: fmt.Sprintf("topic %q not registered; call RegisterPublication first", topic),
		}
	}
	cErr := C.helicsErrorInitialize()
	C.helicsPublicationPublishDouble(pub, C.double(value), &cErr)
	if cErr.error_code != 0 {
		return consumeHelicsError("helicsPublicationPublishDouble", &cErr)
	}
	return nil
}

// Step requests time advance by dt and returns the granted ABSOLUTE
// HELICS time. HELICS' helicsFederateRequestTime takes and returns
// absolute simulation time, not a delta - Step preserves that semantics
// so callers can compare granted vs. requested without an extra
// bookkeeping layer. Example: after Step(100ms) on a fresh federate the
// granted return is 100ms (granted_absolute), not 100ms (delta).
//
// The granted time may be less than current+dt if HELICS forced the
// federate to a smaller step.
//
// Lock discipline: Step holds f.mu only long enough to validate state
// and snapshot the handle, then releases the mutex BEFORE the blocking
// helicsFederateGetCurrentTime / helicsFederateRequestTime calls. A
// stuck peer in HELICS would otherwise wedge Close (which also takes
// f.mu) indefinitely. The handle remains valid for the duration of the
// in-flight cgo call because Close drains f.inFlight before calling
// helicsFederateFree (see Close).
//
// After every successful Step the wrapper signals the subscription
// pump goroutine to drain any updated inputs into their channels.
func (f *Federate) Step(dt time.Duration) (time.Duration, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, ErrFederateClosed
	}
	if f.handle == nil {
		f.mu.Unlock()
		return 0, ErrUninitializedFederate
	}
	handle := f.handle
	// Increment inFlight UNDER the lock and BEFORE releasing it. This
	// pairs with Close's "mark closed under lock, then inFlight.Wait":
	// once Close has the lock and observes f.closed=false, this Step
	// has already incremented inFlight, so Close will wait for it. Once
	// Close sets f.closed=true, no further Step can pass the gate above
	// to increment inFlight.
	f.inFlight.Add(1)
	f.mu.Unlock()
	defer f.inFlight.Done()

	cErr := C.helicsErrorInitialize()
	// HelicsFederateRequestTime takes an absolute time, not a delta.
	currentSeconds := float64(C.helicsFederateGetCurrentTime(handle, &cErr))
	if cErr.error_code != 0 {
		return 0, consumeHelicsError("helicsFederateGetCurrentTime", &cErr)
	}
	requestSeconds := currentSeconds + dt.Seconds()

	granted := C.helicsFederateRequestTime(handle, C.HelicsTime(requestSeconds), &cErr)
	if cErr.error_code != 0 {
		return 0, consumeHelicsError("helicsFederateRequestTime", &cErr)
	}

	// Wake the pump so it drains updated inputs. Non-blocking: a
	// pending trigger means the pump has not caught up yet, which is
	// fine - it will see this Step's updates on its next pass.
	select {
	case f.pumpTrigger <- struct{}{}:
	default:
	}

	return secondsToDuration(float64(granted)), nil
}

// secondsToDuration converts HELICS' double-precision seconds to a
// time.Duration. Saturates at math.MaxInt64 / math.MinInt64 nanoseconds
// rather than relying on the implementation-defined float64->int64
// conversion, which is undefined for values that do not fit (HELICS
// signals out-of-band times via cHelicsBigNumber ~ 9.22e18 sec, far
// above what time.Duration nanoseconds can represent). NaN saturates
// to 0.
func secondsToDuration(s float64) time.Duration {
	if math.IsNaN(s) {
		return 0
	}
	ns := s * float64(time.Second)
	if ns >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	if ns <= float64(math.MinInt64) {
		return time.Duration(math.MinInt64)
	}
	return time.Duration(ns)
}

// pumpUpdates is the goroutine that drains updated subscriptions into
// their channels after each Step. Exits on close(f.pumpDone). Owns the
// channel-close protocol: it is the only goroutine that closes
// subscription channels, and it does so exactly once on shutdown.
func (f *Federate) pumpUpdates() {
	defer f.pumpWG.Done()
	defer f.closeSubscriptionChannels()

	for {
		select {
		case <-f.pumpDone:
			return
		case <-f.pumpTrigger:
			f.drainUpdates()
		}
	}
}

// drainUpdates inspects every subscription under the lock; for each
// updated input it pulls the latest value and pushes a Value onto the
// channel.
//
// Error policy (no in-process error surface yet - #303 will add a
// Federate.Errors() channel):
//
//   - helicsFederateGetCurrentTime failure: skip the entire drain pass
//     and log via slog. We do NOT publish Value{Time:0} for failed
//     timestamps because that is indistinguishable from a legitimate
//     t=0 update (data-invariants rule 1 - silent wrong data).
//   - helicsInputGetDouble failure: skip that one input, log via slog
//     with the topic name, and continue draining the rest. We do NOT
//     publish a sentinel value for the same reason.
//
// The channel send is non-blocking with drop-on-full to give
// latest-value semantics.
func (f *Federate) drainUpdates() {
	f.mu.Lock()
	if f.handle == nil || f.closed {
		f.mu.Unlock()
		return
	}
	handle := f.handle
	// Snapshot to avoid holding the lock across the channel sends.
	subs := make([]subscription, 0, len(f.subscriptions))
	for _, s := range f.subscriptions {
		subs = append(subs, s)
	}
	f.mu.Unlock()

	cErr := C.helicsErrorInitialize()
	currentSeconds := float64(C.helicsFederateGetCurrentTime(handle, &cErr))
	if cErr.error_code != 0 {
		err := consumeHelicsError("helicsFederateGetCurrentTime", &cErr)
		// Skip the entire pass: with no reliable timestamp we cannot
		// stamp Value.Time without forging data. Step's own
		// helicsFederateGetCurrentTime call surfaces the same error
		// to the caller; the pump just silently skips this round.
		slog.Warn("helics: drainUpdates: skipping drain pass",
			"err", err)
		return
	}

	for _, s := range subs {
		if C.helicsInputIsUpdated(s.input) != C.HELICS_TRUE {
			continue
		}
		cErr := C.helicsErrorInitialize()
		v := float64(C.helicsInputGetDouble(s.input, &cErr))
		if cErr.error_code != 0 {
			err := consumeHelicsError("helicsInputGetDouble", &cErr)
			slog.Warn("helics: drainUpdates: dropping update",
				"topic", s.topic, "err", err)
			continue
		}
		val := Value{
			Topic: s.topic,
			Value: v,
			Time:  secondsToDuration(currentSeconds),
		}
		// Drop-on-full: latest value wins. Drain the buffer slot
		// before pushing so we replace rather than discard the new
		// value.
		select {
		case s.ch <- val:
		default:
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- val:
			default:
			}
		}
	}
}

// closeSubscriptionChannels is called exactly once by the pump
// goroutine on shutdown. Closing the channels lets consumers detect
// federate shutdown via the comma-ok receive idiom.
func (f *Federate) closeSubscriptionChannels() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subscriptions {
		close(s.ch)
	}
	// Clear the map so a defensive double-close cannot fire.
	f.subscriptions = nil
}

// Close finalizes and frees the underlying HELICS federate, joins the
// subscription pump goroutine, and Closes any owned in-process broker.
// It is idempotent: the first call performs the teardown and returns
// any HELICS-side finalize error; subsequent calls return
// ErrFederateClosed (recoverable via errors.Is). On a zero-value
// *Federate, Close marks the federate closed and returns
// ErrUninitializedFederate without touching the cgo boundary.
//
// Teardown ordering:
//  1. Mark f.closed=true under f.mu so future Step calls bail with
//     ErrFederateClosed (and cannot increment f.inFlight).
//  2. f.inFlight.Wait - drain any Step calls that already passed the
//     gate and are inside a blocking cgo call against the handle. This
//     is what makes the H1 "release mu before cgo" pattern safe: the
//     handle is not freed until in-flight callers complete.
//  3. close(f.pumpDone); f.pumpWG.Wait - stop and join the pump
//     goroutine so its drainUpdates cgo calls have completed.
//  4. helicsFederateFinalize, helicsFederateFree.
//  5. Close the owned broker (if any).
func (f *Federate) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return ErrFederateClosed
	}
	if f.handle == nil {
		// Zero-value receiver: never constructed. Mark closed so the
		// idempotency contract holds, and skip the cgo path entirely.
		f.closed = true
		f.mu.Unlock()
		return ErrUninitializedFederate
	}
	f.closed = true
	handle := f.handle
	pumpDone := f.pumpDone
	owned := f.ownedBroker
	f.mu.Unlock()

	// Drain in-flight Step calls before freeing the handle. New Step
	// calls cannot increment inFlight because they read f.closed under
	// f.mu and bail; only callers that already passed the gate remain.
	f.inFlight.Wait()

	// Signal the pump to exit and wait for it before freeing the
	// HELICS handle: drainUpdates makes cgo calls without holding f.mu,
	// so freeing while it is mid-call would race the cgo boundary.
	close(pumpDone)
	f.pumpWG.Wait()

	cErr := C.helicsErrorInitialize()
	C.helicsFederateFinalize(handle, &cErr)
	var finalizeErr error
	if cErr.error_code != 0 {
		finalizeErr = consumeHelicsError("helicsFederateFinalize", &cErr)
	}

	// Free regardless of finalize outcome. Free has no error out-param.
	C.helicsFederateFree(handle)

	f.mu.Lock()
	f.handle = nil
	// Publication handles are owned by the federate; free clears them.
	f.publications = nil
	f.mu.Unlock()

	var brokerErr error
	if owned != nil {
		rawErr := owned.Close()
		switch {
		case rawErr == nil:
			// Healthy path.
		case errors.Is(rawErr, ErrBrokerClosed):
			// Owned-broker double-close indicates a wrapper bug
			// (the federate is the only owner). Wrap and surface
			// rather than coerce to nil so the bug is visible.
			brokerErr = fmt.Errorf("owned broker close: %w", rawErr)
		case errors.Is(rawErr, ErrUninitializedBroker):
			// Same: an owned broker is by construction initialized
			// (NewInProcess produced it). Surface as a wrapper bug.
			brokerErr = fmt.Errorf("owned broker close: %w", rawErr)
		default:
			brokerErr = fmt.Errorf("owned broker close: %w", rawErr)
		}
	}

	switch {
	case finalizeErr != nil && brokerErr != nil:
		return fmt.Errorf("helics federate close: %w (also %v)", finalizeErr, brokerErr)
	case finalizeErr != nil:
		return fmt.Errorf("helics federate close: %w", finalizeErr)
	case brokerErr != nil:
		return fmt.Errorf("helics federate close: %w", brokerErr)
	default:
		return nil
	}
}
