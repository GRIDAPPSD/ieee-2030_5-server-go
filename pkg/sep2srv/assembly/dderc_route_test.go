package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
)

// The DefaultDERControl route.
//
// DefaultDERControl is set by the utility (CSIP: the server sets it, clients
// monitor it). PUT is not mounted for this route, so every caller class a
// protocol client can be here (the device the record belongs to, a device
// managing it, and an unrelated device) is refused by the router itself
// before the ownership gate or the handler ever sees the request; GET stays
// open to the owner and its manager exactly as before (#456).

const (
	ddercFSA  = "1"
	ddercDERP = "1"
)

func ddercPath(edevID string) string {
	return "/edev/" + edevID + "/fsa/" + ddercFSA + "/derp/" + ddercDERP + "/dderc"
}

// seedDefaultDERControl stores a DefaultDERControl carrying mrid under
// edevID's dderc key, so a refused write's failure to move it is provable by
// reading the stored value back.
func seedDefaultDERControl(t *testing.T, stores *assembly.Stores, edevID, mrid string) {
	t.Helper()
	key := edevID + "/" + ddercFSA + "/" + ddercDERP
	dc := sep2.DefaultDERControl{MRID: mrid}
	if err := stores.DefaultDERControls.Create(context.Background(), key, coresingleton.SingletonKey, dc); err != nil {
		t.Fatalf("seed DefaultDERControl under %s: %v", key, err)
	}
}

func storedDefaultDERControlMRID(t *testing.T, stores *assembly.Stores, edevID string) string {
	t.Helper()
	key := edevID + "/" + ddercFSA + "/" + ddercDERP
	dc, err := stores.DefaultDERControls.Get(context.Background(), key, coresingleton.SingletonKey)
	if err != nil {
		t.Fatalf("read back DefaultDERControl under %s: %v", key, err)
	}
	return dc.MRID
}

// TestDefaultDERControl_PUTIsRefusedForEveryCallerClass asserts that the
// owner, its manager, and an unrelated device each get PUT refused and each
// leaves the stored value untouched.
func TestDefaultDERControl_PUTIsRefusedForEveryCallerClass(t *testing.T) {
	t.Parallel()

	const original = "ORIGINAL0000000000000000000000000000001"
	forged := `<DefaultDERControl xmlns="urn:ieee:std:2030.5:ns"><mRID>FORGED000000000000000000000000000000002</mRID></DefaultDERControl>`

	cases := []struct {
		name   string
		asLFDI string
		want   int
	}{
		{"owner", victimLFDI, http.StatusMethodNotAllowed},
		{"manager", managerLFDI, http.StatusMethodNotAllowed},
		{"unrelated device", callerLFDI, http.StatusMethodNotAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := newManagementFleet(t).stores
			seedDefaultDERControl(t, stores, victimID, original)
			srv := gateServer(t, stores, gateTestPolicy())

			status, raw := gateRequest(t, srv, http.MethodPut, ddercPath(victimID), tc.asLFDI, forged)
			if status != tc.want {
				t.Fatalf("PUT dderc as %s: status %d, want %d; body=%q", tc.name, status, tc.want, raw)
			}
			if got := storedDefaultDERControlMRID(t, stores, victimID); got != original {
				t.Errorf("PUT dderc as %s changed the stored MRID: got %q, want unchanged %q", tc.name, got, original)
			}
		})
	}
}

// TestDefaultDERControl_PUTRefusalNamesTheServedMethods pins the documented
// status: 405 with an Allow header naming exactly what this route serves.
func TestDefaultDERControl_PUTRefusalNamesTheServedMethods(t *testing.T) {
	t.Parallel()
	stores := newManagementFleet(t).stores
	seedDefaultDERControl(t, stores, victimID, "ORIGINAL0000000000000000000000000000001")
	srv := gateServer(t, stores, gateTestPolicy())

	req, err := http.NewRequest(http.MethodPut, srv.URL+ddercPath(victimID), strings.NewReader(
		`<DefaultDERControl xmlns="urn:ieee:std:2030.5:ns"/>`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(gateIdentityHeader, victimLFDI)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
}

// TestDefaultDERControl_GETStillServesOwnerAndManager asserts that the PUT
// refusal is scoped to PUT: the owner and its manager still read the same
// stored value they always did.
func TestDefaultDERControl_GETStillServesOwnerAndManager(t *testing.T) {
	t.Parallel()
	const mrid = "READBACK000000000000000000000000000003"
	stores := newManagementFleet(t).stores
	seedDefaultDERControl(t, stores, victimID, mrid)
	srv := gateServer(t, stores, gateTestPolicy())

	for _, tc := range []struct {
		name   string
		asLFDI string
	}{
		{"owner", victimLFDI},
		{"manager", managerLFDI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := gateRequest(t, srv, http.MethodGet, ddercPath(victimID), tc.asLFDI, "")
			if status != http.StatusOK {
				t.Fatalf("GET dderc as %s: status %d, want 200; body=%q", tc.name, status, raw)
			}
			var got sep2.DefaultDERControl
			if err := xml.Unmarshal(raw, &got); err != nil {
				t.Fatalf("GET dderc as %s: decode: %v; body=%q", tc.name, err, raw)
			}
			if got.MRID != mrid {
				t.Errorf("GET dderc as %s: MRID = %q, want %q", tc.name, got.MRID, mrid)
			}
		})
	}

	status, raw := gateRequest(t, srv, http.MethodGet, ddercPath(victimID), callerLFDI, "")
	if status != http.StatusForbidden {
		t.Fatalf("GET dderc as unrelated device: status %d, want 403; body=%q", status, raw)
	}
	assertNoFleetData(t, "unrelated device GET dderc", raw)
}
