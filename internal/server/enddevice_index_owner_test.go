package server

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestSeedEndDeviceIndexResumesAboveStoredIDs pins what the index seed does:
// a device already in the store keeps its id, and the next allocation lands
// above it (#443).
func TestSeedEndDeviceIndexResumesAboveStoredIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	devices := memory.NewEndDeviceStore()
	if err := devices.Create(ctx, "7", sep2.EndDevice{SFDI: "1111111111111111", LFDI: "lfdi-7"}); err != nil {
		t.Fatalf("create device 7: %v", err)
	}
	stores := &Stores{}

	seedEndDeviceIndex(stores, devices)

	if got, ok := stores.EndDeviceIndexes.IndexFor("lfdi-7"); !ok || got != "7" {
		t.Errorf("IndexFor(lfdi-7) = (%q, %v), want (\"7\", true)", got, ok)
	}
	if got, err := stores.EndDeviceIndexes.Allocate("lfdi-new"); err != nil || got != "8" {
		t.Errorf("Allocate(lfdi-new) = (%q, %v), want (\"8\", nil)", got, err)
	}
}

// TestEndDeviceIndexSeededOnlyThroughOwner pins that Run and
// NewInMemoryStores reach the index through seedEndDeviceIndex: no other
// function in server.go may build it from the store.
func TestEndDeviceIndexSeededOnlyThroughOwner(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	callers := map[string]int{}
	callsOwner := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				if f.Sel.Name == "NewEndDeviceIndexFromStore" {
					callers[fn.Name.Name]++
				}
			case *ast.Ident:
				if f.Name == "seedEndDeviceIndex" {
					callsOwner[fn.Name.Name] = true
				}
			}
			return true
		})
	}
	if len(callers) != 1 || callers["seedEndDeviceIndex"] != 1 {
		t.Errorf("NewEndDeviceIndexFromStore callers in server.go = %v, want only seedEndDeviceIndex once", callers)
	}
	for _, name := range []string{"Run", "NewInMemoryStores"} {
		if !callsOwner[name] {
			t.Errorf("%s does not seed the EndDevice index through seedEndDeviceIndex", name)
		}
	}
}
