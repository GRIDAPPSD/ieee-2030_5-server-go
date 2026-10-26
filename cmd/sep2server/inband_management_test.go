package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	inBandManager = "AA00000000000000000000000000000000000010"
	inBandManaged = "C900000000000000000000000000000000000009"
)

// postManagedDevice drives SEP2_INBAND_MANAGEMENT through configFromEnv and
// server.NewCoreRouterConfig into the protocol router, then has the manager
// POST /edev for its paired device, and returns the status.
func postManagedDevice(t *testing.T, env string) int {
	t.Helper()
	t.Setenv("SEP2_CERT_DIR", t.TempDir())
	t.Setenv("SEP2_INBAND_MANAGEMENT", env)
	cfg, err := configFromEnv(&certDirResolver{})
	if err != nil {
		t.Fatal(err)
	}

	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(context.Background(), inBandManager, inBandManaged); err != nil {
		t.Fatal(err)
	}
	stores := &assembly.Stores{EndDevices: memory.NewEndDeviceStore(), EndDeviceManagers: managers}
	policy := assembly.AuthPolicy{
		Identity:   func(context.Context) (string, string, bool) { return inBandManager, "111111111116", true },
		SFDIPrefix: func(s string) (string, error) { return s[:8], nil },
	}
	handler, _ := assembly.BuildProtocolRouter(server.NewCoreRouterConfig(cfg), stores, policy, "serverSFDI", "serverLFDI", nil)

	body := `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><lFDI>` + inBandManaged + `</lFDI></EndDevice>`
	req := httptest.NewRequest(http.MethodPost, "/edev", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Code
}

func TestInBandManagementEnvReachesTheRouter(t *testing.T) {
	if got := postManagedDevice(t, ""); got != http.StatusCreated {
		t.Errorf("unset: manager POST status %d, want 201", got)
	}
	if got := postManagedDevice(t, "true"); got != http.StatusCreated {
		t.Errorf("true: manager POST status %d, want 201", got)
	}
	if got := postManagedDevice(t, "false"); got != http.StatusForbidden {
		t.Errorf("false: manager POST status %d, want 403", got)
	}
}

func TestInBandManagementBadValueFailsStartup(t *testing.T) {
	t.Setenv("SEP2_CERT_DIR", t.TempDir())
	t.Setenv("SEP2_INBAND_MANAGEMENT", "maybe")
	_, err := configFromEnv(&certDirResolver{})
	if err == nil || !strings.Contains(err.Error(), "SEP2_INBAND_MANAGEMENT") {
		t.Fatalf("err = %v, want one naming SEP2_INBAND_MANAGEMENT", err)
	}
}
