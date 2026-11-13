package assembly_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// postResponseAt POSTs body as asLFDI and returns the member path the server
// returned in Location.
func postResponseAt(t *testing.T, srv *httptest.Server, asLFDI, body string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/rsps/1/rsp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateIdentityHeader, asLFDI)
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := isolatedClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("control: POST status = %d, want 201", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// responseList reads GET /rsps/1/rsp and returns its all, results and hrefs.
func responseList(t *testing.T, srv *httptest.Server, asLFDI string) (all, results uint32, hrefs []string) {
	t.Helper()
	status, raw := gateRequest(t, srv, http.MethodGet, "/rsps/1/rsp", asLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /rsps/1/rsp = %d", status)
	}
	var list sep2.ResponseList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v: %s", err, raw)
	}
	for _, r := range list.Response {
		hrefs = append(hrefs, r.Href)
	}
	return list.All, list.Results, hrefs
}

// TestDeleteResponse drives DELETE /rsps/{rspsId}/rsp/{rspId} through the real
// router and reads every outcome back through GET.
func TestDeleteResponse(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	t.Run("the poster deletes it and the list shrinks", func(t *testing.T) {
		keep := postResponseAt(t, srv, victimLFDI, responseBody(victimLFDI, rspSubject, "1"))
		gone := postResponseAt(t, srv, victimLFDI, responseBody(victimLFDI, rspSubject, "2"))
		if all, results, _ := responseList(t, srv, victimLFDI); all != 2 || results != 2 {
			t.Fatalf("control: before delete all/results = %d/%d, want 2/2", all, results)
		}
		if status, _ := gateRequest(t, srv, http.MethodDelete, gone, victimLFDI, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, want 204", status)
		}
		if status, _ := gateRequest(t, srv, http.MethodGet, gone, victimLFDI, ""); status != http.StatusNotFound {
			t.Errorf("GET after delete = %d, want 404", status)
		}
		all, results, hrefs := responseList(t, srv, victimLFDI)
		if all != 1 || results != 1 || len(hrefs) != 1 || hrefs[0] != keep {
			t.Errorf("after delete all/results/hrefs = %d/%d/%v, want 1/1/[%s]", all, results, hrefs, keep)
		}
		if status, _ := gateRequest(t, srv, http.MethodDelete, gone, victimLFDI, ""); status != http.StatusNotFound {
			t.Errorf("second DELETE = %d, want 404", status)
		}
	})

	t.Run("another device is refused and nothing is removed", func(t *testing.T) {
		href := postResponseAt(t, srv, victimLFDI, responseBody(victimLFDI, rspSubject, "1"))
		if status, _ := gateRequest(t, srv, http.MethodDelete, href, callerLFDI, ""); status != http.StatusForbidden {
			t.Fatalf("DELETE by another device = %d, want 403", status)
		}
		if status, _ := gateRequest(t, srv, http.MethodDelete, href, "", ""); status != http.StatusForbidden {
			t.Errorf("DELETE with no identity = %d, want 403", status)
		}
		if status, _ := gateRequest(t, srv, http.MethodGet, href, victimLFDI, ""); status != http.StatusOK {
			t.Errorf("GET after refused deletes = %d, want 200", status)
		}
	})

	t.Run("a Response naming no device is deletable by nobody", func(t *testing.T) {
		href := postResponseAt(t, srv, victimLFDI, responseBody("", rspSubject, "1"))
		if status, _ := gateRequest(t, srv, http.MethodDelete, href, victimLFDI, ""); status != http.StatusForbidden {
			t.Fatalf("DELETE of an unowned Response = %d, want 403", status)
		}
		if status, _ := gateRequest(t, srv, http.MethodGet, href, victimLFDI, ""); status != http.StatusOK {
			t.Errorf("GET after refused delete = %d, want 200", status)
		}
	})

	t.Run("a missing id is 404", func(t *testing.T) {
		if status, _ := gateRequest(t, srv, http.MethodDelete, "/rsps/1/rsp/nope", victimLFDI, ""); status != http.StatusNotFound {
			t.Errorf("DELETE of a missing id = %d, want 404", status)
		}
	})
}

// TestDeleteResponseManager pins the manager outcome: the existing Response
// authorization admits the current manager of the device the Response names,
// and no other manager.
func TestDeleteResponseManager(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())
	warmDeviceLog(t, srv, dir, managerLFDI, "/edev/"+victimID)
	warmDeviceLog(t, srv, dir, unmanagedLFDI, "/edev/"+unmanagedID)

	href := postResponseAt(t, srv, managerLFDI, responseBody(victimLFDI, rspSubject, "3"))
	if status, _ := gateRequest(t, srv, http.MethodDelete, href, unmanagedLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("DELETE by an unrelated device = %d, want 403", status)
	}
	// A manager of one device has no say over a Response naming another.
	unmanagedHref := postResponseAt(t, srv, unmanagedLFDI, responseBody(unmanagedLFDI, rspSubject, "4"))
	if status, _ := gateRequest(t, srv, http.MethodDelete, unmanagedHref, managerLFDI, ""); status != http.StatusForbidden {
		t.Fatalf("DELETE by a manager of another device = %d, want 403", status)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, unmanagedHref, unmanagedLFDI, ""); status != http.StatusOK {
		t.Errorf("GET after the refused delete = %d, want 200", status)
	}
	if status, _ := gateRequest(t, srv, http.MethodDelete, href, managerLFDI, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE by the device's manager = %d, want 204", status)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, href, managerLFDI, ""); status != http.StatusNotFound {
		t.Errorf("GET after the manager's delete = %d, want 404", status)
	}
	closeLog()

	// The device log is the copy that survives a restart: the manager's
	// DELETE line carries the subject and status of what it removed and the
	// device it acted for, and the refused DELETE carries none of them.
	_, lines := readLogLines(t, filepath.Join(dir, managerLFDI+".jsonl"))
	var del map[string]any
	for _, l := range lines {
		if l["method"] == http.MethodDelete {
			del = l
		}
	}
	if del == nil {
		t.Fatalf("manager file has no DELETE line: %v", lines)
	}
	if del["status"] != 204.0 || del["subject"] != rspSubject || del["rspStatus"] != 3.0 || del["target"] != victimLFDI {
		t.Errorf("DELETE line status/subject/rspStatus/target = %v/%v/%v/%v, want 204/%s/3/%s",
			del["status"], del["subject"], del["rspStatus"], del["target"], rspSubject, victimLFDI)
	}
	_, other := readLogLines(t, filepath.Join(dir, unmanagedLFDI+".jsonl"))
	var refused map[string]any
	for _, l := range other {
		if l["method"] == http.MethodDelete {
			refused = l
		}
	}
	if refused == nil {
		t.Fatalf("unrelated device's file has no DELETE line: %v", other)
	}
	if refused["status"] != 403.0 {
		t.Errorf("refused DELETE line status = %v, want 403", refused["status"])
	}
	for _, k := range []string{"subject", "subjectInvalid", "rspStatus", "target"} {
		if _, has := refused[k]; has {
			t.Errorf("refused DELETE line carries %s = %v, want none", k, refused[k])
		}
	}
}
