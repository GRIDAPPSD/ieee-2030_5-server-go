// Command graftcannotembedintobody exists only so
// TestEmbeddingCannotSmuggleAFieldIntoBody (../../compile_failure_test.go)
// can prove, by running `go build` against it, that embedding the exported
// TableBody in another package's struct cannot be used to carry an extra
// field into a Descriptor's wire payload. Under the previous design, Body
// was an interface sealed by an unexported METHOD, and embedding TableBody
// promoted that method, so a type like the one below satisfied Body while
// carrying a field the schema never declared. NewTableBody takes a
// TableBody by value, and this type is not a TableBody (it has an extra
// field), so passing it must fail to compile. "testdata" directories are
// skipped by `go build ./...` and `go vet ./...` by convention, so this
// file is never part of the normal build.
package main

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"

type smuggled struct {
	sep2admin.TableBody
	Secret string `json:"secret"`
}

func main() {
	// smuggled embeds TableBody but is not itself a TableBody. If this
	// ever starts compiling, a graft can smuggle an undeclared field
	// into the wire payload again.
	_ = sep2admin.NewTableBody(smuggled{Secret: "smuggled field not in the closed schema"})
}
