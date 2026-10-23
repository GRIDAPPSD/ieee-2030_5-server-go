package assembly_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// servedMRID is the i-th control's mRID, 32 uppercase hex digits.
func servedMRID(i int) string { return fmt.Sprintf("%032X", 0xA0000+i) }

// seedServedControls stores n DERControls under the victim's program, with
// distinct mRIDs and start times so the list order is the creation order. The
// management fleet's own control is removed first so the count is exactly n.
func seedServedControls(t *testing.T, stores *assembly.Stores, n int) {
	t.Helper()
	seedServedControlsStarting(t, stores, n, func(i int) int64 { return int64(1000 + i*1000) })
}

// seedServedControlsStarting is seedServedControls with control i starting at
// start(i), so a test can store controls in a different order than they are
// served.
func seedServedControlsStarting(t *testing.T, stores *assembly.Stores, n int, start func(i int) int64) {
	t.Helper()
	if err := stores.DERControls.Delete(context.Background(), derControlScopeKey(victimID, "1", "1"), victimID); err != nil {
		t.Fatalf("remove the fleet's control: %v", err)
	}
	for i := 0; i < n; i++ {
		c := seedDERControl(t, stores, victimID, fmt.Sprintf("c%02d", i), 100, start(i))
		c.MRID = servedMRID(i)
		if err := stores.DERControls.Update(context.Background(), derControlScopeKey(victimID, "1", "1"), fmt.Sprintf("c%02d", i), c); err != nil {
			t.Fatalf("set mRID: %v", err)
		}
	}
}

func servedLine(t *testing.T, dir, path string) map[string]any {
	t.Helper()
	raw, lines := linesFor(t, filepath.Join(dir, victimLFDI+".jsonl"), path)
	if len(lines) == 0 {
		t.Fatalf("no line for %s: %s", path, raw)
	}
	return lines[len(lines)-1]
}

func mridsOf(t *testing.T, line map[string]any) []string {
	t.Helper()
	raw, ok := line["mrids"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], _ = v.(string)
	}
	return out
}

func TestDeviceLogRecordsServedControlMRIDs(t *testing.T) {
	t.Parallel()
	const listPath = "/edev/1/fsa/1/derp/1/derc"
	cases := []struct {
		name     string
		n        int
		wantN    int
		wantMore float64
	}{
		{"three controls", 3, 3, 0},
		{"ten controls", 10, 8, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := newManagementFleet(t).stores
			seedServedControls(t, stores, tc.n)
			srv, dir, closeLog, _ := logServerFor(t, 1_000_000, gateTestPolicy(), ownerView(stores))
			warmDeviceLog(t, srv, dir, victimLFDI, "/edev/"+victimID)

			status, raw := gateRequest(t, srv, http.MethodGet, listPath+"?l=100", victimLFDI, "")
			if status != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", listPath, status, raw)
			}
			closeLog()

			line := servedLine(t, dir, listPath)
			got := mridsOf(t, line)
			if len(got) != tc.wantN {
				t.Fatalf("mrids = %v, want %d entries", got, tc.wantN)
			}
			for i, m := range got {
				if m != servedMRID(i) {
					t.Errorf("mrids[%d] = %q, want %q", i, m, servedMRID(i))
				}
			}
			more, present := line["mridsMore"]
			if tc.wantMore == 0 && present {
				t.Errorf("mridsMore = %v, want absent", more)
			}
			if tc.wantMore != 0 && more != tc.wantMore {
				t.Errorf("mridsMore = %v, want %v", more, tc.wantMore)
			}
		})
	}
}

func TestDeviceLogRecordsSingleControlAndDefaultControlMRID(t *testing.T) {
	t.Parallel()
	stores := newManagementFleet(t).stores
	seedServedControls(t, stores, 2)
	const defaultMRID = "DEFA0000000000000000000000000001"
	seedDefaultDERControl(t, stores, victimID, defaultMRID)
	srv, dir, closeLog, _ := logServerFor(t, 1_000_000, gateTestPolicy(), ownerView(stores))
	warmDeviceLog(t, srv, dir, victimLFDI, "/edev/"+victimID)

	onePath := "/edev/1/fsa/1/derp/1/derc/c01"
	for _, p := range []string{onePath, ddercPath(victimID)} {
		if status, raw := gateRequest(t, srv, http.MethodGet, p, victimLFDI, ""); status != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", p, status, raw)
		}
	}
	closeLog()

	one := servedLine(t, dir, onePath)
	if got := mridsOf(t, one); len(got) != 1 || got[0] != servedMRID(1) {
		t.Errorf("single control mrids = %v, want [%s]", got, servedMRID(1))
	}
	def := servedLine(t, dir, ddercPath(victimID))
	if got := mridsOf(t, def); len(got) != 1 || got[0] != defaultMRID {
		t.Errorf("DefaultDERControl mrids = %v, want [%s]", got, defaultMRID)
	}
	for _, l := range []map[string]any{one, def} {
		if _, present := l["mridsMore"]; present {
			t.Errorf("mridsMore present on %v, want absent", l["path"])
		}
	}
}

// TestServedStoreNotesNothingOnTheUndecoratedStore proves an admin-style read
// of the store the router was built from adds nothing to a request's note, and
// a missing control notes nothing on the protocol route.
func TestServedStoreNotesNothingOnTheUndecoratedStore(t *testing.T) {
	t.Parallel()
	stores := newManagementFleet(t).stores
	seedServedControls(t, stores, 3)
	srv, dir, closeLog, _ := logServerFor(t, 1_000_000, gateTestPolicy(), ownerView(stores))
	warmDeviceLog(t, srv, dir, victimLFDI, "/edev/"+victimID)

	ctx, note := devicelog.WithNote(context.Background())
	res, err := stores.DERControls.List(ctx, derControlScopeKey(victimID, "1", "1"), store.ListOptions{Unbounded: true})
	if err != nil || len(res.Items) != 3 {
		t.Fatalf("control: admin-style List = %d items, err %v, want 3", len(res.Items), err)
	}
	if got := note.Fields(); len(got) != 0 {
		t.Errorf("admin-style read left note fields %v, want none", got)
	}

	missing := "/edev/1/fsa/1/derp/1/derc/nope"
	if status, _ := gateRequest(t, srv, http.MethodGet, missing, victimLFDI, ""); status != http.StatusNotFound {
		t.Fatalf("control: GET %s = %d, want 404", missing, status)
	}
	closeLog()
	line := servedLine(t, dir, missing)
	if _, present := line["mrids"]; present {
		t.Errorf("404 line carries mrids %v, want none", line["mrids"])
	}
}

// TestDeviceLogRecordsOnlyTheServedPage stores 10 controls in the opposite
// order to their start times and pages through them, so the page served is
// not the first of the store's own order. The line must name exactly the
// controls in the body, in the order served.
func TestDeviceLogRecordsOnlyTheServedPage(t *testing.T) {
	t.Parallel()
	const listPath = "/edev/1/fsa/1/derp/1/derc"
	stores := newManagementFleet(t).stores
	seedServedControlsStarting(t, stores, 10, func(i int) int64 { return int64(20000 - i*1000) })
	srv, dir, closeLog, _ := logServerFor(t, 1_000_000, gateTestPolicy(), ownerView(stores))
	warmDeviceLog(t, srv, dir, victimLFDI, "/edev/"+victimID)

	// Served order is by start ascending, so control 9 comes first.
	pages := []struct {
		query string
		want  []string
	}{
		{"?s=0&l=1", []string{servedMRID(9)}},
		{"?s=0&l=3", []string{servedMRID(9), servedMRID(8), servedMRID(7)}},
		{"?s=3&l=3", []string{servedMRID(6), servedMRID(5), servedMRID(4)}},
	}
	for _, pg := range pages {
		status, raw := gateRequest(t, srv, http.MethodGet, listPath+pg.query, victimLFDI, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s%s = %d: %s", listPath, pg.query, status, raw)
		}
		// Control: the body itself serves exactly this page, in this order.
		last := -1
		for _, m := range pg.want {
			at := strings.Index(string(raw), m)
			if at <= last {
				t.Fatalf("control: body of %s%s does not serve %s after the previous one: %s", listPath, pg.query, m, raw)
			}
			last = at
		}
		if n := strings.Count(string(raw), "<mRID>"); n != len(pg.want) {
			t.Fatalf("control: body of %s%s serves %d controls, want %d", listPath, pg.query, n, len(pg.want))
		}
	}
	closeLog()

	_, lines := linesFor(t, filepath.Join(dir, victimLFDI+".jsonl"), listPath)
	var pageLines []map[string]any
	for _, l := range lines {
		if l["method"] == http.MethodGet {
			pageLines = append(pageLines, l)
		}
	}
	if len(pageLines) != len(pages) {
		t.Fatalf("got %d list lines, want %d", len(pageLines), len(pages))
	}
	for i, pg := range pages {
		if m := mridsOf(t, pageLines[i]); !reflect.DeepEqual(m, pg.want) {
			t.Errorf("page %q mrids = %v, want the served page %v", pg.query, m, pg.want)
		}
		if more, present := pageLines[i]["mridsMore"]; present {
			t.Errorf("page %q mridsMore = %v, want absent", pg.query, more)
		}
	}
}
