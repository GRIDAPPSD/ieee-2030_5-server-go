package dcap_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/dcap"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

func TestHandleDeviceCapabilityGET(t *testing.T) {
	h := dcap.HandleDeviceCapability(allWired, nil)
	req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/sep+xml" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/sep+xml")
	}

	var dc sep2.DeviceCapability
	if err := xml.Unmarshal(w.Body.Bytes(), &dc); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if dc.Href != "/dcap" {
		t.Errorf("Href = %q, want %q", dc.Href, "/dcap")
	}
	if dc.PollRate != 900 {
		t.Errorf("PollRate = %d, want %d", dc.PollRate, 900)
	}
	if dc.TimeLink == nil || dc.TimeLink.Href != "/tm" {
		t.Error("TimeLink should point to /tm")
	}
	if dc.SelfDeviceLink == nil || dc.SelfDeviceLink.Href != "/sdev" {
		t.Error("SelfDeviceLink should point to /sdev")
	}

	body := w.Body.String()
	if !strings.Contains(body, "urn:ieee:std:2030.5:ns") {
		t.Error("response should contain IEEE 2030.5 namespace")
	}
}

func TestHandleDeviceCapabilityPOST(t *testing.T) {
	h := dcap.HandleDeviceCapability(allWired, nil)
	req := httptest.NewRequest(http.MethodPost, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleDeviceCapabilityHEAD(t *testing.T) {
	h := dcap.HandleDeviceCapability(allWired, nil)
	req := httptest.NewRequest(http.MethodHead, "/dcap", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want %d", w.Code, http.StatusOK)
	}
}

func allWired(routefamily.Anchor) bool { return true }

func getDcap(t *testing.T, h http.HandlerFunc) sep2.DeviceCapability {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dcap", nil))
	var dc sep2.DeviceCapability
	if err := xml.Unmarshal(w.Body.Bytes(), &dc); err != nil {
		t.Fatalf("Unmarshal response: %v; body=%s", err, w.Body.String())
	}
	return dc
}

func TestHandleDeviceCapabilityLinksOnlyWhatIsWired(t *testing.T) {
	t.Parallel()

	dc := getDcap(t, dcap.HandleDeviceCapability(nil, nil))
	if dc.EndDeviceListLink == nil || dc.EndDeviceListLink.Href != "/edev" ||
		dc.TimeLink == nil || dc.SelfDeviceLink == nil {
		t.Errorf("a nil Wired must still link the families that need no store: %+v", dc)
	}
	if dc.MessagingProgramListLink != nil || dc.ResponseSetListLink != nil ||
		dc.UsagePointListLink != nil || dc.MirrorUsagePointListLink != nil {
		t.Errorf("a nil Wired linked an anchored family: %+v", dc)
	}

	onlyUsage := func(a routefamily.Anchor) bool { return a == routefamily.AnchorUsagePoints }
	dc = getDcap(t, dcap.HandleDeviceCapability(onlyUsage, nil))
	if dc.UsagePointListLink == nil || dc.UsagePointListLink.Href != "/upt" {
		t.Errorf("UsagePointListLink = %+v, want /upt", dc.UsagePointListLink)
	}
	if dc.MirrorUsagePointListLink != nil || dc.MessagingProgramListLink != nil {
		t.Errorf("linked a family whose anchor is not wired: %+v", dc)
	}
}

func TestHandleDeviceCapabilityAllFollowsTheCounter(t *testing.T) {
	t.Parallel()

	counts := map[string]uint32{"A": 3, "B": 1}
	counter := func(r *http.Request) (uint32, bool, error) {
		n, ok := counts[r.Header.Get("X-Caller")]
		return n, ok, nil
	}
	h := dcap.HandleDeviceCapability(allWired, counter)
	for caller, want := range counts {
		req := httptest.NewRequest(http.MethodGet, "/dcap", nil)
		req.Header.Set("X-Caller", caller)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var dc sep2.DeviceCapability
		if err := xml.Unmarshal(w.Body.Bytes(), &dc); err != nil {
			t.Fatal(err)
		}
		if dc.EndDeviceListLink == nil || dc.EndDeviceListLink.All != want {
			t.Errorf("caller %s: EndDeviceListLink = %+v, want all %d", caller, dc.EndDeviceListLink, want)
		}
	}
	if dc := getDcap(t, h); dc.EndDeviceListLink == nil || dc.EndDeviceListLink.All != 0 {
		t.Errorf("unknown caller: EndDeviceListLink = %+v, want the link without all", dc.EndDeviceListLink)
	}
}

func TestHandleDeviceCapabilityStalledCountDoesNotHoldTheResponse(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	stalled := func(*http.Request) (uint32, bool, error) {
		<-release
		return 9, true, nil
	}
	start := time.Now()
	dc := getDcap(t, dcap.HandleDeviceCapability(allWired, stalled))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("response took %v behind a stalled count", elapsed)
	}
	if dc.EndDeviceListLink == nil || dc.EndDeviceListLink.Href != "/edev" || dc.EndDeviceListLink.All != 0 {
		t.Errorf("EndDeviceListLink = %+v, want the link without all", dc.EndDeviceListLink)
	}
}
