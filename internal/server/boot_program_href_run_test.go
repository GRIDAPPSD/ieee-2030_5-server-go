package server_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// A persisted program whose href names no FSA, and that no boot fixture
// names, refuses the boot with the program named (#916) instead of being
// served under no FSA without a word.
func TestRunRefusesPersistedProgramWithoutFSA(t *testing.T) {
	dataDir := t.TempDir()
	programs, err := memory.NewDERProgramStoreWithPersistence(filepath.Join(dataDir, "derprograms.json"))
	if err != nil {
		t.Fatalf("open program store: %v", err)
	}
	prog := sep2.DERProgram{MRID: "OLD-SHAPE"}
	prog.Href = "/edev/e1/derp/p1"
	if err := programs.Create(context.Background(), "e1", "p1", prog); err != nil {
		t.Fatalf("persist program: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = server.Run(ctx, &config.Config{Addr: "127.0.0.1:0", DataDir: dataDir}, nil)
	if !errors.Is(err, bootfixture.ErrDERProgramWithoutFSA) {
		t.Fatalf("Run err = %v, want ErrDERProgramWithoutFSA", err)
	}
}
