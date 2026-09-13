package bootfixture_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/bootfixture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

const (
	sfdiE1    = "111111111111"
	sfdiOther = "999999999999"
)

func assertNoSFDI(t *testing.T, text string) {
	t.Helper()
	for _, sfdi := range []string{sfdiE1, sfdiOther} {
		if strings.Contains(text, sfdi) {
			t.Errorf("text carries an SFDI: %q", text)
		}
	}
}

func createPersisted(t *testing.T, s store.EndDeviceStore, id, lfdi, sfdi string) {
	t.Helper()
	dev := sep2.EndDevice{LFDI: lfdi, SFDI: sfdi, ChangedTime: 7}
	dev.Href = "/edev/" + id
	if err := s.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("pre-populate %s: %v", id, err)
	}
}

// Criterion 2e: an unseeded fixture EndDevice whose SFDI or LFDI another
// persisted EndDevice holds stops the boot before any write, whether or not
// the fixture id is already persisted.
func TestReconcileHeldIdentifierFailsBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		populate  func(t *testing.T, s store.EndDeviceStore)
		wantWhich string
	}{
		{
			name: "SFDI held by a different LFDI, fixture id absent",
			populate: func(t *testing.T, s store.EndDeviceStore) {
				createPersisted(t, s, "x", lfdiOther, sfdiE1)
			},
			wantWhich: "SFDI",
		},
		{
			name: "SFDI held by a different LFDI, fixture id adopted",
			populate: func(t *testing.T, s store.EndDeviceStore) {
				createPersisted(t, s, "e1", lfdiE1, "555555555555")
				createPersisted(t, s, "x", lfdiOther, sfdiE1)
			},
			wantWhich: "SFDI",
		},
		{
			name: "LFDI held by a different id, fixture id adopted",
			populate: func(t *testing.T, s store.EndDeviceStore) {
				createPersisted(t, s, "e1", lfdiE1, sfdiE1)
				createPersisted(t, s, "x", strings.ToLower(lfdiE1), sfdiOther)
			},
			wantWhich: "LFDI",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newSeedHarness(t)
			ctx := context.Background()

			edevs, _ := h.reopen()
			tc.populate(t, edevs)
			before := h.snapshotDataDir()

			target, err := h.boot(fixtureV1)
			if err == nil {
				t.Fatal("boot succeeded, want an identity conflict")
			}
			if !errors.Is(err, bootfixture.ErrIdentityConflict) {
				t.Errorf("error chain lacks ErrIdentityConflict: %v", err)
			}
			for _, want := range []string{`end_devices[0] (id="e1")`, "its " + tc.wantWhich + " is held", `"/edev/x"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			assertNoLFDI(t, err.Error())
			assertNoSFDI(t, err.Error())

			h.assertDataDirUnchanged(before)
			reopened, derps := h.reopen()
			if tc.wantWhich == "SFDI" {
				// The live index is what registration reads; a create would
				// have repointed it at the fixture device.
				for which, s := range map[string]store.EndDeviceStore{"live": target.EndDevices, "reopened": reopened} {
					got, err := s.GetBySFDI(ctx, sfdiE1)
					if err != nil || got.Href != "/edev/x" {
						t.Errorf("%s GetBySFDI = %q, %v, want /edev/x", which, got.Href, err)
					}
				}
			}
			x := mustEndDevice(t, reopened, "x")
			if x.ChangedTime != 7 {
				t.Errorf("holder x ChangedTime = %d, want it unchanged", x.ChangedTime)
			}
			_, err = derps.Get(ctx, "e1", "p1")
			assertNotFound(t, err, "DERProgram (e1, p1)")
		})
	}
}

// The held-identifier checks apply only to records not yet seeded: a device
// that later takes a seeded record's SFDI does not stop every boot after.
func TestReconcileSeededRecordSkipsHeldIdentifierCheck(t *testing.T) {
	t.Parallel()
	h := newSeedHarness(t)

	h.mustBoot(fixtureV1)
	edevs, _ := h.reopen()
	createPersisted(t, edevs, "x", lfdiOther, sfdiE1)

	h.mustBoot(fixtureV1)

	edevs, derps := h.reopen()
	assertFixtureE1(t, mustEndDevice(t, edevs, "e1"))
	x := mustEndDevice(t, edevs, "x")
	if x.LFDI != lfdiOther || x.SFDI != sfdiE1 || x.ChangedTime != 7 {
		t.Errorf("x = LFDI %q SFDI %q ChangedTime %d, want it unchanged", x.LFDI, x.SFDI, x.ChangedTime)
	}
	assertFixtureP1(t, mustProgram(t, derps, "e1", "p1"))
	h.assertSeedKeys(edevKey("e1"), derpKey("e1", "p1"))
}
