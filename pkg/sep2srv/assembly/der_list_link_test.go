package assembly_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// A device that registered itself through POST /edev was served an EndDevice
// with no DERListLink and had no DER under it, so a client walking links had
// nowhere to PUT its DERStatus. This test is that client: it registers, then
// reaches DERStatus only by following the links it is served.
func TestDERListLink_SelfRegisteredDeviceReachesDERStatusByLinks(t *testing.T) {
	t.Parallel()

	srv, _ := indexTestServer(t, filepath.Join(t.TempDir(), "edevindex.json"))
	created := register(t, srv, deviceLFDIA)
	if created.DERListLink == nil {
		t.Fatal("POST /edev response: DERListLink is nil")
	}

	status, body := do(t, srv, http.MethodGet, created.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d; body=%s", created.Href, status, body)
	}
	var dev sep2.EndDevice
	if err := xml.Unmarshal([]byte(body), &dev); err != nil {
		t.Fatalf("decode EndDevice: %v; body=%s", err, body)
	}
	if dev.DERListLink == nil {
		t.Fatalf("GET %s: DERListLink is nil; body=%s", created.Href, body)
	}
	if want := created.Href + "/der"; dev.DERListLink.Href != want {
		t.Errorf("DERListLink.Href = %q, want %q", dev.DERListLink.Href, want)
	}
	if dev.DERListLink.All != 1 {
		t.Errorf("DERListLink.all = %d, want 1 (the seeded DER)", dev.DERListLink.All)
	}

	status, body = do(t, srv, http.MethodGet, dev.DERListLink.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d; body=%s", dev.DERListLink.Href, status, body)
	}
	var list sep2.DERList
	if err := xml.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode DERList: %v; body=%s", err, body)
	}
	if len(list.DER) != 1 {
		t.Fatalf("DERList has %d members, want 1; body=%s", len(list.DER), body)
	}
	statusLink := list.DER[0].DERStatusLink
	if statusLink == nil {
		t.Fatalf("DER has no DERStatusLink; body=%s", body)
	}

	alarm := sep2.HexBinary32(0)
	ders := sep2.DERStatus{AlarmStatus: &alarm, ReadingTime: 1700000000}
	payload, err := xml.Marshal(&ders)
	if err != nil {
		t.Fatalf("marshal DERStatus: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+statusLink.Href, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	req.Header.Set("X-Test-LFDI", deviceLFDIA)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", statusLink.Href, err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: status %d; body=%s", statusLink.Href, resp.StatusCode, respBody)
	}

	status, body = do(t, srv, http.MethodGet, statusLink.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d; body=%s", statusLink.Href, status, body)
	}
	var served sep2.DERStatus
	if err := xml.Unmarshal([]byte(body), &served); err != nil {
		t.Fatalf("decode DERStatus: %v; body=%s", err, body)
	}
	if served.ReadingTime != ders.ReadingTime {
		t.Errorf("DERStatus readingTime = %d, want %d", served.ReadingTime, ders.ReadingTime)
	}
}
