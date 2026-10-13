package adminplane

import (
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The DER control lifecycle store is held as the store.ScopedStore interface,
// which has no Persists method, so Persisted must still see a persistent one.
func TestNewAdminDERControlHandler_PersistedFollowsBothStores(t *testing.T) {
	cases := []struct {
		name                 string
		persistentControls   bool
		persistentLifecycles bool
		wantPersisted        bool
	}{
		{"both persistent", true, true, true},
		{"controls in memory", false, true, false},
		{"lifecycles in memory", true, false, false},
		{"both in memory", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := derControlWiringStores()
			if tc.persistentControls {
				c, err := memory.NewDERControlStoreWithPersistence(filepath.Join(dir, "dercontrols.json"))
				if err != nil {
					t.Fatalf("DER control store: %v", err)
				}
				s.DERControls = c
			}
			if tc.persistentLifecycles {
				l, err := dercontrol.NewLifecycleStoreWithPersistence(filepath.Join(dir, "lifecycles.json"))
				if err != nil {
					t.Fatalf("lifecycle store: %v", err)
				}
				s.DERControlLifecycles = l
			}
			h := newAdminDERControlHandler(s)
			if h == nil {
				t.Fatal("newAdminDERControlHandler = nil over fully wired stores")
			}
			if h.Persisted != tc.wantPersisted {
				t.Errorf("Persisted = %v, want %v", h.Persisted, tc.wantPersisted)
			}
		})
	}
}
