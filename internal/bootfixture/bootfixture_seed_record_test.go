package bootfixture_test

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A seed record with no records, or with a key whose shape does not fit its
// kind, fails the boot: the server never writes either, and reading one would
// recreate a record deleted since it was seeded.
func TestReconcileRejectsMalformedSeedRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{name: "records absent", body: `{"version":1}`},
		{name: "records null", body: `{"version":1,"records":null}`},
		{name: "records empty", body: `{"version":1,"records":[]}`},
		{name: "EndDevice key with a parent", body: `{"version":1,"records":[{"kind":"EndDevice","parent":"x","id":"e1"}]}`},
		{name: "DERProgram key without a parent", body: `{"version":1,"records":[{"kind":"DERProgram","parent":"","id":"p1"}]}`},
		{name: "key without an id", body: `{"version":1,"records":[{"kind":"EndDevice","parent":"","id":""}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newSeedHarness(t)
			ctx := context.Background()

			h.mustBoot(fixtureV1)
			edevs, _ := h.reopen()
			if err := edevs.Delete(ctx, "e1"); err != nil {
				t.Fatalf("operator Delete e1: %v", err)
			}
			if err := os.WriteFile(h.path(seedRecordFile), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write seed record: %v", err)
			}
			before := h.snapshotDataDir()

			_, err := h.boot(fixtureV1)
			if err == nil {
				t.Fatal("boot succeeded, want a seed record error")
			}
			if !strings.Contains(err.Error(), seedRecordFile) {
				t.Errorf("error %q does not cite the seed record path", err)
			}
			h.assertDataDirUnchanged(before)
			edevs, _ = h.reopen()
			_, err = edevs.Get(ctx, "e1")
			assertNotFound(t, err, "EndDevice e1 deleted before the boot")
		})
	}
}
