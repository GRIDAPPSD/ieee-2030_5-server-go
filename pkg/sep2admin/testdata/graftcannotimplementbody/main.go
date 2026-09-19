// Command graftcannotimplementbody exists only so
// TestGraftCannotImplementBody (../../compile_failure_test.go) can prove,
// by running `go build` against it, that Body is sealed to package
// sep2admin: bodyKind is unexported, so a type declared in another
// package cannot satisfy Body even by spelling the same method name.
// "testdata" directories are skipped by `go build ./...` and
// `go vet ./...` by convention, so this file is never part of the
// normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

type fakeBody struct{}

func (fakeBody) bodyKind() string { return "fake" }

func main() {
	// bodyKind is unexported: a same-named method declared outside
	// package sep2admin does not satisfy sep2admin.Body. If this ever
	// starts compiling, Body's seal is broken.
	var b sep2admin.Body = fakeBody{}
	_ = b
}
