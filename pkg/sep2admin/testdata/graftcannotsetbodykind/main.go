// Command graftcannotsetbodykind exists only so the table-driven compile
// failure test (../../compile_failure_test.go) can prove, by running `go
// build` against it, that Body.kind is unreachable from outside package
// sep2admin: the same mechanism proven for Placement.group in
// graftcannotsetgroup, applied to Body's own seal.
// "testdata" directories are skipped by `go build ./...` and
// `go vet ./...` by convention, so this file is never part of the
// normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

func main() {
	// kind is unexported: naming it in a composite literal from another
	// package must fail to compile. If this ever starts compiling,
	// Body's seal is broken and a caller could construct any bodyKind
	// value directly.
	_ = sep2admin.Body{kind: 1}
}
