// Package derwrite owns the server's writes to device DER programs and their
// controls. It holds no HTTP, JSON or XML, so the admin plane and the protocol
// plane can both call it.
package derwrite

import "sync"

// Owner is the one writer of device DER programs, shared by the admin API and
// the protocol plane. A process builds exactly one: two owners are two locks,
// and a program write on one could interleave with an assignment on the other
// and leave a device with a missing or duplicated template copy. The zero
// value is ready to use.
//
// It exposes Lock and Unlock rather than Serialize(fn) because handlers hold
// the lock across early returns and error paths, and callers pair Unlock
// with defer.
type Owner struct {
	mu sync.Mutex
}

// Lock takes the lock that serializes every program write with assignments,
// fleet FSAs, grid blocks and requests, FSA controls and device controls.
func (o *Owner) Lock() { o.mu.Lock() }

// Unlock releases the lock taken by Lock.
func (o *Owner) Unlock() { o.mu.Unlock() }
