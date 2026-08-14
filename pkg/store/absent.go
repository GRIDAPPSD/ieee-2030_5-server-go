package store

import "reflect"

// IsAbsent reports whether a store handle names no store.
//
// # Why this exists, and why a plain nil comparison is not enough
//
// A server decides which function sets it serves by which store handles it was
// given: a nil handle means the function set is not wired, and IEEE 2030.5-2018
// section 4.4 p.19 requires such a server to advertise no link to it and to
// mount no route for it. While every handle was a concrete pointer, that
// decision was a plain nil comparison and it was correct.
//
// Once the handles are interface-typed it stops being correct, and it stops
// being correct SILENTLY. An interface value holds a type and a pointer, so a
// nil *memory.ScopedStore assigned into a ScopedStore field yields an interface
// that is NOT equal to nil: the type half is set. A consumer that leaves a
// field out of its store constructor, which is the ordinary way of saying "I do
// not serve this", would have its routes mounted over a handle that panics on
// first use. The failure is a 500 from a route that should never have existed,
// on a deployment that never asked for it.
//
// The reflection is the price of that being detectable at all. There is no
// comparison and no type switch that distinguishes a nil interface from an
// interface holding a nil pointer; only the reflect package can see the
// difference.
//
// # What it treats as absent
//
// A nil interface, and an interface holding a nil pointer, map, slice, channel,
// function or interface. Anything else is present. A non-pointer value store,
// which no implementation here uses, is present: it cannot be nil, and treating
// a valid zero-sized implementation as absent would silently unmount the
// function set it serves.
//
// It is a mount-time question, not a request-time one. Call it where a server
// decides what to serve; do not call it per request, where the answer cannot
// have changed and the reflection is pure cost.
func IsAbsent(handle any) bool {
	if handle == nil {
		return true
	}
	v := reflect.ValueOf(handle)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}
