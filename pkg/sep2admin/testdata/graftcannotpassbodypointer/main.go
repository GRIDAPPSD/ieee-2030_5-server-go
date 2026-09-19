// Command graftcannotpassbodypointer exists only so
// TestBodyConstructorsRejectPointers (../../compile_failure_test.go) can
// prove, by running `go build` against it, that a *TableBody cannot reach
// Descriptor.Body. Under the previous interface-based design, both
// TableBody and *TableBody satisfied Body (bodyKind had a value receiver),
// so a nil *TableBody assigned to Body panicked json.Marshal. NewTableBody
// takes a TableBody by value, so a pointer is a type mismatch, not merely
// a discouraged call: there is no way to construct a Body from a pointer
// at all. "testdata" directories are skipped by `go build ./...` and
// `go vet ./...` by convention, so this file is never part of the normal
// build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

func main() {
	var t *sep2admin.TableBody
	// If this ever starts compiling, a nil *TableBody could reach
	// Descriptor.Body again and panic json.Marshal.
	_ = sep2admin.NewTableBody(t)
}
