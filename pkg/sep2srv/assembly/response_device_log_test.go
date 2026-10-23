package assembly_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

const rspSubject = "0123456789ABCDEF0123456789ABCDEF"

func responseBody(lfdi, subject, status string) string {
	var b strings.Builder
	b.WriteString(`<Response xmlns="urn:ieee:std:2030.5:ns">`)
	if lfdi != "" {
		b.WriteString("<endDeviceLFDI>" + lfdi + "</endDeviceLFDI>")
	}
	if status != "" {
		b.WriteString("<status>" + status + "</status>")
	}
	if subject != "" {
		b.WriteString("<subject>" + subject + "</subject>")
	}
	b.WriteString("</Response>")
	return b.String()
}

func postResponse(t *testing.T, srvURL, asLFDI, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+"/rsps/1/rsp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateIdentityHeader, asLFDI)
	req.Header.Set("Content-Type", "application/sep+xml")
	status, _ := sendGateRequest(t, req)
	return status
}

// TestDeviceLogRecordsWhatEachResponseSaid reads the written lines back by
// value: the subject uppercased, the status, the managed device as target,
// and nothing for a refused or malformed POST.
func TestDeviceLogRecordsWhatEachResponseSaid(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())
	warmDeviceLog(t, srv, dir, managerLFDI, "/edev/"+victimID)
	warmDeviceLog(t, srv, dir, unmanagedLFDI, "/edev/"+unmanagedID)

	lower := strings.ToLower(rspSubject)
	posts := []struct {
		name, as, body string
		want           int
	}{
		{"manager for managed device", managerLFDI, responseBody(victimLFDI, lower, "3"), http.StatusCreated},
		{"device for itself", unmanagedLFDI, responseBody(unmanagedLFDI, rspSubject, "1"), http.StatusCreated},
		{"non-hex subject", unmanagedLFDI, responseBody(unmanagedLFDI, "ZZZZ-not-hex", "2"), http.StatusCreated},
		{"forbidden: not sender or manager", unmanagedLFDI, responseBody(victimLFDI, rspSubject, "3"), http.StatusForbidden},
		{"bad request: not an LFDI", unmanagedLFDI, responseBody("zz", rspSubject, "3"), http.StatusBadRequest},
	}
	for _, p := range posts {
		if got := postResponse(t, srv.URL, p.as, p.body); got != p.want {
			t.Fatalf("control: %s status = %d, want %d", p.name, got, p.want)
		}
	}
	closeLog()

	var rsp []map[string]any
	_, mgr := readLogLines(t, filepath.Join(dir, managerLFDI+".jsonl"))
	for _, l := range mgr {
		if l["method"] == http.MethodPost {
			rsp = append(rsp, l)
		}
	}
	if len(rsp) != 1 {
		t.Fatalf("manager file has %d POST lines, want 1: %v", len(rsp), mgr)
	}
	m := rsp[0]
	if m["subject"] != rspSubject || m["rspStatus"] != 3.0 || m["target"] != victimLFDI {
		t.Errorf("manager line subject/rspStatus/target = %v/%v/%v, want %s/3/%s", m["subject"], m["rspStatus"], m["target"], rspSubject, victimLFDI)
	}

	_, dev := readLogLines(t, filepath.Join(dir, unmanagedLFDI+".jsonl"))
	var own, invalid, forbidden, bad map[string]any
	for _, l := range dev {
		if l["method"] != http.MethodPost {
			continue
		}
		switch {
		case l["status"] == 403.0:
			forbidden = l
		case l["status"] == 400.0:
			bad = l
		case l["subjectInvalid"] != nil:
			invalid = l
		default:
			own = l
		}
	}
	if own == nil || own["subject"] != rspSubject || own["rspStatus"] != 1.0 {
		t.Errorf("own line = %v, want subject %s and rspStatus 1", own, rspSubject)
	}
	if _, has := own["target"]; has {
		t.Errorf("a device's own Response carries target %v, want none", own["target"])
	}
	if invalid == nil || invalid["subjectInvalid"] != true || invalid["rspStatus"] != 2.0 {
		t.Errorf("invalid-subject line = %v, want subjectInvalid true and rspStatus 2", invalid)
	}
	if _, has := invalid["subject"]; has {
		t.Errorf("non-hex subject was recorded as text: %v", invalid["subject"])
	}
	for name, l := range map[string]map[string]any{"403": forbidden, "400": bad} {
		if l == nil {
			t.Fatalf("no %s line found in %v", name, dev)
		}
		for _, k := range []string{"subject", "subjectInvalid", "rspStatus", "target"} {
			if _, has := l[k]; has {
				t.Errorf("%s POST line carries %s = %v, want none", name, k, l[k])
			}
		}
	}

	// The managed device's own view is its manager's lines naming it.
	rd, err := devicelog.NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := rd.Read(context.Background(), devicelog.Query{
		Sources: []devicelog.Source{{File: managerLFDI, Actors: map[string]bool{managerLFDI: true}, Target: victimLFDI, Label: "manager"}},
		Limit:   50,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page.Entries {
		if strings.Contains(string(e.Line), `"subject":"`+rspSubject+`"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("managed device's view has no Response line: %d entries", len(page.Entries))
	}
}
