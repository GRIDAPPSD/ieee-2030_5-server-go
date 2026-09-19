// Command graftcannotsettable exists only so the table-driven compile
// failure test (../../compile_failure_test.go) can prove, by running `go
// build` against it, that Body.table is unreachable from outside package
// sep2admin: the same mechanism proven for kind in graftcannotsetbodykind,
// applied to the other two fields the "no second field to set" guarantee
// depends on.
// "testdata" directories are skipped by `go build ./...` and
// `go vet ./...` by convention, so this file is never part of the
// normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

func main() {
	// table is unexported: naming it in a composite literal from another
	// package must fail to compile. If this ever starts compiling, a
	// Body already carrying one shape could have a second shape set on
	// it from outside the package.
	_ = sep2admin.Body{table: sep2admin.TableBody{}}
}
