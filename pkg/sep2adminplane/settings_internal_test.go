package sep2adminplane

import (
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

func TestUnsetSettingsResolveToServerDefaults(t *testing.T) {
	edition, deadline, grace, err := resolveSettings(Config{Stores: &assembly.Stores{}})
	if err != nil {
		t.Fatalf("resolveSettings: %v", err)
	}
	if edition != handler.Edition2018 || deadline != 300*time.Second || grace != 1800*time.Second {
		t.Fatalf("resolved %s, %s, %s; want 2018, 5m0s, 30m0s", edition, deadline, grace)
	}
}
