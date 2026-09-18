// Command graftcannotsetgroup exists only so
// TestGraftCannotNameTheCoreBand (../../compile_failure_test.go) can prove,
// by running `go build` against it, that Placement.group is unreachable
// from outside package sep2admin. "testdata" directories are skipped by
// `go build ./...` and `go vet ./...` by convention, so this file is never
// part of the normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

func main() {
	// group is unexported: naming it in a composite literal from another
	// package must fail to compile. If this ever starts compiling,
	// criterion 3 of issue #367 is broken.
	_ = sep2admin.Placement{group: 1}
}
