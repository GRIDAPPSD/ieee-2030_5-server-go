// #566 criterion 9: the BASIC-008, BASIC-009 and BASIC-010 procedures with
// their DERControl created through the admin API instead of seeded by the
// fixture. Each fixture's topology is loaded, its seeded control removed,
// and the procedure's test-value control (CSIP V1.2 Figures 8, 9 and 10)
// posted to the admin create handler. A device then walks the protocol path
// and reads it back.
package csip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// adminDERControlAdvance moves the server clock forward. It is nil in the
// untagged build, which has no clock hook; csip_test_hooks sets it.
var adminDERControlAdvance func(t *testing.T, d time.Duration)

const (
	adminDERControlLeadSeconds     = 120 // "effective start time of (plus-two minutes)"
	adminDERControlDurationSeconds = 60  // "duration (plus-one minute)"
	adminDERControlPEN             = uint32(0x0000A0B1)
)

var adminDERControlMRID = regexp.MustCompile(`^[0-9A-F]{32}$`)

type adminDERControlCase struct {
	name     string
	fixture  string
	typeJSON string // the type and value members of the create body
	assert   func(t *testing.T, cipher string, base *sep2.DERControlBase)
}

func TestAdminCreatedControl_BASIC_008_009_010(t *testing.T) {
	cases := []adminDERControlCase{
		{
			// Figure 8 test values: displacement 900, excitation false, multiplier -3.
			name:     "BASIC-008",
			fixture:  "basic-008-fixed-pf.yaml",
			typeJSON: `"type":"fixedPFInjectW","powerFactor":{"displacement":900,"excitation":false}`,
			assert: func(t *testing.T, cipher string, base *sep2.DERControlBase) {
				t.Helper()
				pf := base.OpModFixedPFInjectW
				if pf == nil || pf.Displacement != 900 || pf.Excitation || pf.Multiplier != -3 {
					t.Errorf("[%s] opModFixedPFInjectW = %+v, want displacement 900 excitation false multiplier -3", cipher, pf)
				}
			},
		},
		{
			// Figure 9 test values: opModEnergize true, opModConnect true.
			name:     "BASIC-009",
			fixture:  "basic-009-connect-disconnect.yaml",
			typeJSON: `"type":"connect"`,
			assert: func(t *testing.T, cipher string, base *sep2.DERControlBase) {
				t.Helper()
				if base.OpModEnergize == nil || !*base.OpModEnergize || base.OpModConnect == nil || !*base.OpModConnect {
					t.Errorf("[%s] opModEnergize = %v opModConnect = %v, want true and true", cipher, base.OpModEnergize, base.OpModConnect)
				}
			},
		},
		{
			// Figure 10 test value: opModMaxLimW 6000.
			name:     "BASIC-010",
			fixture:  "basic-010-limit-max-active-p.yaml",
			typeJSON: `"type":"maxLimW","maxLimW":6000`,
			assert: func(t *testing.T, cipher string, base *sep2.DERControlBase) {
				t.Helper()
				if base.OpModMaxLimW == nil || *base.OpModMaxLimW != 6000 {
					t.Errorf("[%s] opModMaxLimW = %v, want 6000", cipher, base.OpModMaxLimW)
				}
			},
		},
	}

	// The harness offers CCM-8 only (csiptest.BootServer); the mode table
	// keeps the shape the other BASIC tests use.
	modes := []struct {
		name string
		opts []csiptest.BootOption
	}{{name: "CCM", opts: nil}}

	for _, tc := range cases {
		for _, mode := range modes {
			// Not parallel: under csip_test_hooks the clock advance below is
			// process-global.
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				runAdminDERControlProcedure(t, tc, mode.name, mode.opts)
			})
		}
	}
}

func runAdminDERControlProcedure(t *testing.T, tc adminDERControlCase, cipher string, extraOpts []csiptest.BootOption) {
	ctx := context.Background()
	stores := csiptest.NewFreshStores()
	stores.DERControlLifecycles = dercontrol.NewLifecycleStore()
	target := &csiptest.Target{
		EndDevices:         stores.EndDevices,
		FSAs:               stores.FSAs,
		DERPrograms:        stores.DERPrograms,
		DERControls:        stores.DERControls,
		DefaultDERControls: stores.DefaultDERControls,
		DERCurves:          stores.DERCurves,
	}
	owner := csiptest.NewDeviceIdentity(t, "ADMIN-DERC-DEVICE")
	if err := csiptest.Load(ctx, target, filepath.Join("fixtures", tc.fixture), csiptest.Bind(fixtureEndDeviceID, owner)); err != nil {
		t.Fatalf("load %s: %v", tc.fixture, err)
	}
	removeSeededControls(t, ctx, stores.DERControls)

	opts := append([]csiptest.BootOption{csiptest.WithStores(stores), csiptest.WithDeviceIdentity(owner)}, extraOpts...)
	srv := csiptest.BootServer(t, opts...)
	client := srv.Client()

	prog := walkSingleProgramBasic(t, ctx, client)
	if prog.DERControlListLink == nil {
		t.Fatalf("[%s] fixture program has no DERControlListLink", cipher)
	}

	issuer, err := dercontrol.NewIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, dercontrol.Config{PEN: ptrPEN(adminDERControlPEN)})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	admin := &handler.AdminDERControlHandler{
		Issuer:     issuer,
		Controls:   stores.DERControls,
		Lifecycles: stores.DERControlLifecycles,
		Programs:   stores.DERPrograms,
		EndDevices: stores.EndDevices,
		Responses:  stores.Responses,
	}

	// The CSIP loader stores a program's own href without its FSA segment
	// (csiptest.buildDERProgram), so the program is addressed through its
	// DERControlListLink, which carries the full path.
	programHref := strings.TrimSuffix(prog.DERControlListLink.Href, "/derc")
	start := sep2time.Now().Unix() + adminDERControlLeadSeconds
	body := fmt.Sprintf(`{"derProgramHref":%q,%s,"startTime":%d,"durationSeconds":%d}`, programHref, tc.typeJSON, start, adminDERControlDurationSeconds)
	created := adminDERControlCall(t, admin.HandleCreate(), http.MethodPost, "/api/der/controls", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("[%s] admin create: status %d body %s", cipher, created.Code, created.Body.String())
	}
	var view handler.DERControlCreated
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatalf("[%s] decode create: %v", cipher, err)
	}

	list := walkSingleControlListBasic(t, ctx, client, prog)
	if list.All != 1 || len(list.DERControl) != 1 {
		t.Fatalf("[%s] DERControlList all = %d len = %d, want the one created control", cipher, list.All, len(list.DERControl))
	}
	dc := list.DERControl[0]
	if dc.DERControlBase == nil {
		t.Fatalf("[%s] DERControlBase is nil", cipher)
	}
	tc.assert(t, cipher, dc.DERControlBase)
	if !adminDERControlMRID.MatchString(dc.MRID) || dc.MRID != view.MRID {
		t.Errorf("[%s] mRID = %q, want 32 hex digits equal to the admin response %q", cipher, dc.MRID, view.MRID)
	}
	if dc.CreationTime == 0 || dc.CreationTime != view.CreationTime {
		t.Errorf("[%s] creationTime = %d, want nonzero and equal to the admin response %d", cipher, dc.CreationTime, view.CreationTime)
	}
	if dc.Interval == nil || dc.Interval.Start != start || dc.Interval.Duration != adminDERControlDurationSeconds {
		t.Errorf("[%s] interval = %+v, want start %d duration %d", cipher, dc.Interval, start, adminDERControlDurationSeconds)
	}
	if dc.ResponseRequired == nil || *dc.ResponseRequired != 0x07 || dc.ReplyTo == "" {
		t.Errorf("[%s] responseRequired = %v replyTo = %q, want 07 and a replyTo", cipher, dc.ResponseRequired, dc.ReplyTo)
	}
	if dc.EventStatus == nil || dc.EventStatus.CurrentStatus != sep2.EventStatusScheduled {
		t.Errorf("[%s] EventStatus = %+v before start, want currentStatus 0", cipher, dc.EventStatus)
	}

	var single sep2.DERControl
	if err := client.WalkLink(ctx, sep2.Link{Href: dc.Href}, &single); err != nil {
		t.Fatalf("[%s] GET %s: %v", cipher, dc.Href, err)
	}
	if got, want := marshalControl(t, &single), marshalControl(t, &dc); !bytes.Equal(got, want) {
		t.Errorf("[%s] single-resource bytes differ from the list member\nsingle: %s\nlist:   %s", cipher, got, want)
	}

	progAfter := walkSingleProgramBasic(t, ctx, client)
	if progAfter.DERControlListLink == nil || progAfter.DERControlListLink.All != 1 {
		t.Errorf("[%s] DERControlListLink after create = %+v, want all 1", cipher, progAfter.DERControlListLink)
	}

	if adminDERControlAdvance != nil {
		adminDERControlAdvance(t, (adminDERControlLeadSeconds+5)*time.Second)
		var after sep2.DERControl
		if err := client.WalkLink(ctx, sep2.Link{Href: dc.Href}, &after); err != nil {
			t.Fatalf("[%s] GET after advance: %v", cipher, err)
		}
		if after.EventStatus == nil || after.EventStatus.CurrentStatus != sep2.EventStatusActive {
			t.Errorf("[%s] EventStatus after advancing past start = %+v, want currentStatus 1", cipher, after.EventStatus)
		}
	}

	postDERControlResponse(t, srv, dc.ReplyTo, owner.LFDI, dc.MRID, 1)
	listed := adminDERControlCall(t, admin.HandleList(), http.MethodGet, "/api/der/controls?device="+fixtureEndDeviceID, "")
	var got handler.DERControlList
	if err := json.Unmarshal(listed.Body.Bytes(), &got); err != nil || len(got.Controls) != 1 {
		t.Fatalf("[%s] admin list = %s (%v), want one control", cipher, listed.Body.String(), err)
	}
	if r := got.Controls[0].Responses; r.Total != 1 || r.ByStatus["1"] != 1 {
		t.Errorf("[%s] admin list responses = %+v, want the device's one status-1 response", cipher, r)
	}
}

// removeSeededControls deletes every control the fixture seeded, so the only
// control a device can reach is the one the admin API creates.
func removeSeededControls(t *testing.T, ctx context.Context, controls store.ScopedStore[sep2.DERControl]) {
	t.Helper()
	parents, err := controls.Parents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, p := range parents {
		page, err := controls.List(ctx, p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Items {
			id := c.Href[strings.LastIndex(c.Href, "/")+1:]
			if err := controls.Delete(ctx, p, id); err != nil {
				t.Fatalf("delete seeded control %s: %v", c.Href, err)
			}
			removed++
		}
	}
	if removed == 0 {
		t.Fatal("fixture seeded no control to remove; the topology no longer matches this test")
	}
}

func adminDERControlCall(t *testing.T, h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func marshalControl(t *testing.T, c *sep2.DERControl) []byte {
	t.Helper()
	b, err := xml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// postDERControlResponse posts a Response to replyTo as the device would.
func postDERControlResponse(t *testing.T, srv *csiptest.BootedServer, replyTo, lfdi, subject string, status uint8) {
	t.Helper()
	st := status
	body, err := xml.Marshal(&sep2.Response{EndDeviceLFDI: lfdi, Status: &st, Subject: subject, CreatedDateTime: sep2time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.BaseURL+replyTo, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := srv.HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", replyTo, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: status %d, want 201", replyTo, resp.StatusCode)
	}
}

func ptrPEN(v uint32) *uint32 { return &v }
