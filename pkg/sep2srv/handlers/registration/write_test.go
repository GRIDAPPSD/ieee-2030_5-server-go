package registration_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/registration"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func writeMux(regs *memory.RegistrationStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}/rg", registration.HandlePutRegistration(regs))
	mux.HandleFunc("DELETE /edev/{id}/rg", registration.HandleDeleteRegistration(regs))
	return mux
}

func send(mux http.Handler, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/edev/"+deviceID+"/rg", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func regBody(pin string) string {
	return `<Registration xmlns="urn:ieee:std:2030.5:ns"><dateTimeRegistered>5</dateTimeRegistered><pIN>` + pin + `</pIN></Registration>`
}

func TestPutRegistrationCreatesServerAuthoredRecord(t *testing.T) {
	regs := memory.NewRegistrationStore()
	w := send(writeMux(regs), http.MethodPut, regBody("424242"))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/edev/"+deviceID+"/rg" {
		t.Errorf("Location = %q, want the canonical registration href", got)
	}
	got, err := regs.Get(context.Background(), deviceID)
	if err != nil {
		t.Fatalf("stored registration: %v", err)
	}
	if got.PIN != 424242 {
		t.Errorf("PIN = %d, want 424242", got.PIN)
	}
	if got.Href != "/edev/"+deviceID+"/rg" {
		t.Errorf("Href = %q, want the server-minted href", got.Href)
	}
	if got.PollRate != memory.DefaultRegistrationPollRate {
		t.Errorf("PollRate = %d, want %d", got.PollRate, memory.DefaultRegistrationPollRate)
	}
	if got.DateTimeRegistered <= 5 {
		t.Errorf("DateTimeRegistered = %d, want the server's clock, not the body's 5", got.DateTimeRegistered)
	}
}

func TestPutRegistrationReplacesOnlyThePIN(t *testing.T) {
	regs := memory.NewRegistrationStore()
	edevs := memory.NewEndDeviceStore()
	seedRegistrationFixture(t, edevs, regs)
	if err := regs.Update(context.Background(), deviceID, sep2.Registration{
		Resource: sep2.Resource{Href: "/edev/" + deviceID + "/rg"}, DateTimeRegistered: 1700000000, PIN: 123456, PollRate: 60,
	}); err != nil {
		t.Fatal(err)
	}

	w := send(writeMux(regs), http.MethodPut,
		`<Registration xmlns="urn:ieee:std:2030.5:ns" href="/edev/other/rg" pollRate="5"><dateTimeRegistered>9</dateTimeRegistered><pIN>7</pIN></Registration>`)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	got, _ := regs.Get(context.Background(), deviceID)
	want := sep2.Registration{Resource: sep2.Resource{Href: "/edev/" + deviceID + "/rg"}, DateTimeRegistered: 1700000000, PIN: 7, PollRate: 60}
	if got.PIN != want.PIN || got.Href != want.Href || got.DateTimeRegistered != want.DateTimeRegistered || got.PollRate != want.PollRate {
		t.Errorf("stored = %+v, want %+v", got, want)
	}
}

func TestPutRegistrationRefusesBadBodiesAndLeavesTheStoreAlone(t *testing.T) {
	cases := []struct{ name, body string }{
		{"pin above six digits", regBody("1000000")},
		{"malformed xml", "<Registration"},
		{"empty body", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			regs := memory.NewRegistrationStore()
			w := send(writeMux(regs), http.MethodPut, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if _, err := regs.Get(context.Background(), deviceID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a refused PUT left a registration: err = %v", err)
			}
		})
	}
}

func TestPutRegistrationAcceptsTheLargestPIN(t *testing.T) {
	regs := memory.NewRegistrationStore()
	if w := send(writeMux(regs), http.MethodPut, regBody("999999")); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	got, _ := regs.Get(context.Background(), deviceID)
	if got.PIN != 999999 {
		t.Errorf("PIN = %d, want 999999", got.PIN)
	}
}

func TestDeleteRegistrationReturnsAndRemovesTheRecord(t *testing.T) {
	regs := memory.NewRegistrationStore()
	edevs := memory.NewEndDeviceStore()
	seedRegistrationFixture(t, edevs, regs)

	w := send(writeMux(regs), http.MethodDelete, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got sep2.Registration
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.PIN != 123456 || got.Href != "/edev/"+deviceID+"/rg" {
		t.Errorf("body = pin %d href %q, want the removed record", got.PIN, got.Href)
	}
	if _, err := regs.Get(context.Background(), deviceID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("registration still stored after DELETE: err = %v", err)
	}
	if w := send(writeMux(regs), http.MethodDelete, ""); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", w.Code)
	}
}

func TestRegistrationWritesRefuseOtherMethods(t *testing.T) {
	regs := memory.NewRegistrationStore()
	for method, h := range map[string]http.HandlerFunc{
		http.MethodPut:    registration.HandleDeleteRegistration(regs),
		http.MethodDelete: registration.HandlePutRegistration(regs),
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(method, "/edev/"+deviceID+"/rg", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, w.Code)
		}
	}
}

func persistedRegs(t *testing.T) (*memory.RegistrationStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registrations.json")
	regs, err := memory.NewRegistrationStoreWithPersistence(path)
	if err != nil {
		t.Fatal(err)
	}
	return regs, path
}

// TestPutRegistrationConcurrentFirstPutsNeverFail sends many first PUTs at
// once: exactly one creates (201), the rest replace (204), and none is a 500.
func TestPutRegistrationConcurrentFirstPutsNeverFail(t *testing.T) {
	regs, _ := persistedRegs(t)
	mux := writeMux(regs)
	const n = 24
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = send(mux, http.MethodPut, regBody("424242")).Code
		}()
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusNoContent:
		default:
			t.Errorf("status = %d, want 201 or 204", c)
		}
	}
	if created != 1 {
		t.Errorf("%d PUTs answered 201, want exactly 1", created)
	}
}

// TestPutRegistrationRacingDeleteNeverFails runs PUTs against DELETEs: a PUT
// is 201 or 204 and a DELETE is 200 or 404, never a 500.
func TestPutRegistrationRacingDeleteNeverFails(t *testing.T) {
	regs, _ := persistedRegs(t)
	mux := writeMux(regs)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if c := send(mux, http.MethodPut, regBody("1")).Code; c != http.StatusCreated && c != http.StatusNoContent {
				t.Errorf("PUT status = %d, want 201 or 204", c)
			}
		}()
		go func() {
			defer wg.Done()
			if c := send(mux, http.MethodDelete, "").Code; c != http.StatusOK && c != http.StatusNotFound {
				t.Errorf("DELETE status = %d, want 200 or 404", c)
			}
		}()
	}
	wg.Wait()
}

func blockSnapshot(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestRegistrationWritesThatCannotPersistAnswer500AndChangeNothing pins that a
// failed save is neither reported as success nor left standing in memory.
func TestRegistrationWritesThatCannotPersistAnswer500AndChangeNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("first PUT", func(t *testing.T) {
		regs, path := persistedRegs(t)
		blockSnapshot(t, path)
		if w := send(writeMux(regs), http.MethodPut, regBody("1")); w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", w.Code)
		}
		if _, err := regs.Get(ctx, deviceID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the failed PUT left a record: err = %v", err)
		}
	})

	t.Run("replacing PUT", func(t *testing.T) {
		regs, path := persistedRegs(t)
		if w := send(writeMux(regs), http.MethodPut, regBody("111")); w.Code != http.StatusCreated {
			t.Fatalf("seed status = %d", w.Code)
		}
		blockSnapshot(t, path)
		if w := send(writeMux(regs), http.MethodPut, regBody("222")); w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", w.Code)
		}
		if got, _ := regs.Get(ctx, deviceID); got.PIN != 111 {
			t.Errorf("pin = %d, want the prior 111", got.PIN)
		}
	})

	t.Run("DELETE", func(t *testing.T) {
		regs, path := persistedRegs(t)
		if w := send(writeMux(regs), http.MethodPut, regBody("111")); w.Code != http.StatusCreated {
			t.Fatalf("seed status = %d", w.Code)
		}
		blockSnapshot(t, path)
		if w := send(writeMux(regs), http.MethodDelete, ""); w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", w.Code)
		}
		if got, err := regs.Get(ctx, deviceID); err != nil || got.PIN != 111 {
			t.Errorf("after a failed DELETE the record is (%+v, %v), want it still stored", got, err)
		}
	})
}
