// Command graftcannotsetdefinitionlist exists only so the table-driven
// compile failure test (../../compile_failure_test.go) can prove, by
// running `go build` against it, that Body.definitionList is unreachable
// from outside package sep2admin: the third and last of the fields the
// "no second field to set" guarantee depends on.
// "testdata" directories are skipped by `go build ./...` and
// `go vet ./...` by convention, so this file is never part of the
// normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

func main() {
	// definitionList is unexported: naming it in a composite literal
	// from another package must fail to compile.
	_ = sep2admin.Body{definitionList: sep2admin.DefinitionListBody{}}
}
