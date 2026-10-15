package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
)

// Each FSA lists only the programs whose own href names it (#916). Device e1
// has two FSAs with distinct programs; device e2 has a single FSA that shares
// e1's FSA id, so a filter that ignored the device scope would leak e2's
// programs into e1's list.
func seedTwoFSAPrograms(t *testing.T) *httptest.Server {
	t.Helper()
	stores := testStores()
	ctx := context.Background()
	seedOwnedDevices(t, stores.EndDevices, "e1", "e2")

	programs := []struct {
		edev, fsa, id, mrid string
		primacy             uint8
	}{
		{"e1", "f1", "p1", "a1", 2},
		{"e1", "f1", "p3", "a3", 0},
		{"e1", "f1", "p4", "a4", 1},
		{"e1", "f2", "p2", "b2", 0},
		{"e2", "f1", "q1", "c1", 0},
		{"e2", "f1", "q2", "c2", 5},
		// An href with no FSA segment, as written before #743: no FSA lists it.
		{"e1", "", "old", "z9", 0},
	}
	for _, p := range programs {
		prog := sep2.DERProgram{MRID: p.mrid, Primacy: p.primacy}
		prog.Href = coreder.DERProgramHref(p.edev, p.fsa, p.id)
		if p.fsa == "" {
			prog.Href = "/edev/" + p.edev + "/derp/" + p.id
		}
		if err := stores.DERPrograms.Create(ctx, p.edev, p.id, prog); err != nil {
			t.Fatalf("seed DERProgram %s/%s: %v", p.edev, p.id, err)
		}
	}

	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestDERProgramList_EachFSAListsOnlyItsOwnPrograms(t *testing.T) {
	t.Parallel()
	srv := seedTwoFSAPrograms(t)

	cases := []struct {
		path        string
		all         uint32
		wantMRIDs   []string
		wantHrefFSA string
	}{
		// Table 50 order within the filtered set: primacy asc, then mRID desc.
		{"/edev/e1/fsa/f1/derp?l=255", 3, []string{"a3", "a4", "a1"}, "/edev/e1/fsa/f1/derp/"},
		{"/edev/e1/fsa/f2/derp?l=255", 1, []string{"b2"}, "/edev/e1/fsa/f2/derp/"},
		{"/edev/e1/fsa/f9/derp?l=255", 0, nil, ""},
		// A device with one FSA sees every one of its programs.
		{"/edev/e2/fsa/f1/derp?l=255", 2, []string{"c1", "c2"}, "/edev/e2/fsa/f1/derp/"},
		// Paging cuts from the filtered, ordered set, and all counts that set.
		{"/edev/e1/fsa/f1/derp?s=1&l=1", 3, []string{"a4"}, "/edev/e1/fsa/f1/derp/"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			l := getList[sep2.DERProgramList](t, srv, tc.path)
			var got []string
			for _, m := range l.DERProgram {
				got = append(got, m.MRID)
				if !strings.HasPrefix(m.Href, tc.wantHrefFSA) {
					t.Errorf("member href = %q, want prefix %q", m.Href, tc.wantHrefFSA)
				}
			}
			if l.All != tc.all || l.Results != uint32(len(tc.wantMRIDs)) || !slices.Equal(got, tc.wantMRIDs) {
				t.Errorf("all=%d results=%d mRIDs=%v, want all=%d results=%d %v",
					l.All, l.Results, got, tc.all, len(tc.wantMRIDs), tc.wantMRIDs)
			}
		})
	}
}

func TestDERProgramMember_OtherFSAIsNotFound(t *testing.T) {
	t.Parallel()
	srv := seedTwoFSAPrograms(t)

	cases := []struct {
		path     string
		wantCode int
		wantMRID string
	}{
		{"/edev/e1/fsa/f1/derp/p1", http.StatusOK, "a1"},
		{"/edev/e1/fsa/f2/derp/p2", http.StatusOK, "b2"},
		{"/edev/e1/fsa/f2/derp/p1", http.StatusNotFound, ""},
		{"/edev/e1/fsa/f1/derp/p2", http.StatusNotFound, ""},
		{"/edev/e1/fsa/f9/derp/p1", http.StatusNotFound, ""},
		{"/edev/e1/fsa/f1/derp/q1", http.StatusNotFound, ""},
		{"/edev/e1/fsa/f1/derp/old", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := srv.Client().Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			if resp.StatusCode != tc.wantCode {
				_ = resp.Body.Close()
				t.Fatalf("GET %s status = %d, want %d", tc.path, resp.StatusCode, tc.wantCode)
			}
			if tc.wantCode != http.StatusOK {
				_ = resp.Body.Close()
				return
			}
			var p sep2.DERProgram
			decodeXML(t, resp, &p)
			if p.MRID != tc.wantMRID || p.Href != tc.path {
				t.Errorf("GET %s = mRID %q href %q, want mRID %q href %q", tc.path, p.MRID, p.Href, tc.wantMRID, tc.path)
			}
		})
	}
}
