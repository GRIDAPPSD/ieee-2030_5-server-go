// Command readstorescannotmutate exists only so
// TestReadStoresCannotMutate (../../compile_failure_test.go) can prove, by
// running `go build` against it, that assembly.ReaderStores (returned by
// Server.ReadStores) admits no method that writes. "testdata" directories
// are skipped by `go build ./...` and `go vet ./...` by convention, so this
// file is never part of the normal build.
package main

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2server"
)

func main() {
	var srv *sep2server.Server
	reader := srv.ReadStores()

	// Create is a write; MirrorUsagePoints on ReaderStores is typed as
	// store.ResourceReader, which has no such method. If this ever starts
	// compiling, criterion 2 of issue #343 is broken.
	_ = reader.MirrorUsagePoints.Create(context.Background(), "x", sep2.MirrorUsagePoint{})
}
