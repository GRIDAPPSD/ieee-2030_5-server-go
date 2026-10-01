// Command graftcannotsetchart exists only so the table-driven compile
// failure test (../../compile_failure_test.go) can prove, by running `go
// build` against it, that Body.chart is unreachable from outside package
// sep2admin.
// "testdata" directories are skipped by `go build ./...` and
// `go vet ./...` by convention, so this file is never part of the
// normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

func main() {
	// chart is unexported: naming it in a composite literal from another
	// package must fail to compile, or a Body already carrying a table
	// could have a chart set on it too.
	_ = sep2admin.Body{chart: sep2admin.ChartBody{}}
}
