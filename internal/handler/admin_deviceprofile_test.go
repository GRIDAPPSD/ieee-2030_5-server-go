package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	profileLFDIA = "AAAA000000000000000000000000000000000001"
	profileLFDIB = "BBBB000000000000000000000000000000000002"
)

// failingProfiles refuses every Put with err and records nothing.
type failingProfiles struct {
	store.DeviceProfileStore
	err error
}

func (f failingProfiles) Put(context.Context, store.DeviceProfile) error { return f.err }

func postAddDevice(t *testing.T, devs store.EndDeviceStore, regs handler.RegistrationWriter, profiles store.DeviceProfileStore, extra string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	sfdi, lfdi := freshAddDeviceCert(t)
	body := fmt.Sprintf(`{"sfdi":%q,"lfdi":%q,"pin":1,"enabled":true%s}`, sfdi, lfdi, extra)
	req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleAddEndDevice(devs, regs, profiles)(w, req)
	return w, lfdi
}

func TestAddEndDeviceRefusesBadNameAndKind(t *testing.T) {
	ctx := context.Background()
	profiles := memory.NewDeviceProfileStore()
	if w, _ := postAddDevice(t, memory.NewEndDeviceStore(), &stubRegs{}, profiles, `,"name":"Inverter 3"`); w.Code != http.StatusCreated {
		t.Fatalf("seed add: %d %s", w.Code, w.Body.String())
	}

	cases := []struct {
		name  string
		extra string
		code  int
	}{
		{"33 octets", `,"name":"` + strings.Repeat("a", 33) + `"`, http.StatusBadRequest},
		{"control character", `,"name":"a\u0007b"`, http.StatusBadRequest},
		{"non-ASCII", ",\"name\":\"caf\xc3\xa9\"", http.StatusBadRequest},
		{"leading space", `,"name":" lead"`, http.StatusBadRequest},
		{"trailing space", `,"name":"trail "`, http.StatusBadRequest},
		{"duplicate ignoring case", `,"name":"INVERTER 3"`, http.StatusConflict},
		{"unknown kind", `,"name":"ok","kind":"inverter"`, http.StatusBadRequest},
		{"old description field", `,"description":"x"`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			devs := memory.NewEndDeviceStore()
			regs := &stubRegs{}
			w, lfdi := postAddDevice(t, devs, regs, profiles, c.extra)
			if w.Code != c.code {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, c.code, w.Body.String())
			}
			if _, err := devs.GetByLFDI(ctx, lfdi); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("EndDevice left behind after a refusal: err = %v", err)
			}
			if regs.created {
				t.Error("Registration left behind after a refusal")
			}
			if strings.Contains(w.Body.String(), "INVERTER 3") {
				t.Errorf("refusal echoed the name: %s", w.Body.String())
			}
		})
	}

	list, err := profiles.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "Inverter 3" {
		t.Errorf("profiles after refusals = %+v, %v; want only the seeded one", list, err)
	}
}

func TestAddEndDeviceProfileWriteFailureLeavesNothing(t *testing.T) {
	devs := memory.NewEndDeviceStore()
	regs := &stubRegs{}
	profiles := failingProfiles{DeviceProfileStore: memory.NewDeviceProfileStore(), err: errors.New("disk full")}

	w, lfdi := postAddDevice(t, devs, regs, profiles, `,"name":"Roof","kind":"aggregator"`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "disk full") {
		t.Errorf("response carries the store error text: %s", w.Body.String())
	}
	if _, err := devs.GetByLFDI(context.Background(), lfdi); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("EndDevice survived the profile failure: err = %v", err)
	}
	if regs.created {
		t.Error("Registration survived the profile failure")
	}
}

func TestAddEndDeviceNameConflictAtWriteRollsBack(t *testing.T) {
	devs := memory.NewEndDeviceStore()
	regs := &stubRegs{}
	profiles := failingProfiles{DeviceProfileStore: memory.NewDeviceProfileStore(), err: store.ErrAlreadyExists}

	w, lfdi := postAddDevice(t, devs, regs, profiles, `,"name":"Roof"`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if _, err := devs.GetByLFDI(context.Background(), lfdi); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("EndDevice survived the name conflict: err = %v", err)
	}
	if regs.created {
		t.Error("Registration survived the name conflict")
	}
}

func TestAddEndDeviceWithoutNameOrKindWritesNoProfile(t *testing.T) {
	profiles := memory.NewDeviceProfileStore()
	if w, _ := postAddDevice(t, memory.NewEndDeviceStore(), &stubRegs{}, profiles, ``); w.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	list, _ := profiles.List(context.Background())
	if len(list) != 0 {
		t.Errorf("profiles = %+v, want none", list)
	}
}

func TestAddEndDeviceRequiresProfileStore(t *testing.T) {
	var absent *memory.DeviceProfileStore
	w, _ := postAddDevice(t, memory.NewEndDeviceStore(), &stubRegs{}, absent, ``)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for a nil profile store", w.Code)
	}
}

func putProfile(h http.HandlerFunc, lfdi, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/device-profiles/"+lfdi, strings.NewReader(body))
	req.SetPathValue("lfdi", lfdi)
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestPutDeviceProfileStoresAndLists(t *testing.T) {
	profiles := memory.NewDeviceProfileStore()
	h := &handler.AdminDeviceProfileHandler{Profiles: profiles}

	w := putProfile(h.HandlePutDeviceProfile(), strings.ToLower(profileLFDIB), `{"name":"Meter 9","kind":"device"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body = %s", w.Code, w.Body.String())
	}
	if w := putProfile(h.HandlePutDeviceProfile(), profileLFDIA, `{"name":"Gateway","kind":"aggregator"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT A status = %d; body = %s", w.Code, w.Body.String())
	}

	rec := httptest.NewRecorder()
	h.HandleListDeviceProfiles()(rec, httptest.NewRequest(http.MethodGet, "/api/device-profiles", nil))
	var got struct {
		Profiles []store.DeviceProfile `json:"profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v; body = %s", err, rec.Body.String())
	}
	want := []store.DeviceProfile{
		{LFDI: profileLFDIA, Name: "Gateway", Kind: "aggregator"},
		{LFDI: profileLFDIB, Name: "Meter 9", Kind: "device"},
	}
	if len(got.Profiles) != len(want) || got.Profiles[0] != want[0] || got.Profiles[1] != want[1] {
		t.Errorf("profiles = %+v, want %+v (sorted by LFDI, LFDI upper-cased)", got.Profiles, want)
	}
}

func TestPutDeviceProfileRefusals(t *testing.T) {
	profiles := memory.NewDeviceProfileStore()
	h := &handler.AdminDeviceProfileHandler{Profiles: profiles}
	if w := putProfile(h.HandlePutDeviceProfile(), profileLFDIA, `{"name":"Taken"}`); w.Code != http.StatusOK {
		t.Fatalf("seed: %d", w.Code)
	}

	cases := []struct {
		name, lfdi, body string
		code             int
	}{
		{"short lfdi", "abc", `{"name":"x"}`, http.StatusBadRequest},
		{"33 octets", profileLFDIB, `{"name":"` + strings.Repeat("z", 33) + `"}`, http.StatusBadRequest},
		{"padded", profileLFDIB, `{"name":" x "}`, http.StatusBadRequest},
		{"non-printable", profileLFDIB, `{"name":"a\nb"}`, http.StatusBadRequest},
		{"duplicate ignoring case", profileLFDIB, `{"name":"tAkEn"}`, http.StatusConflict},
		{"unknown kind", profileLFDIB, `{"name":"x","kind":"gateway"}`, http.StatusBadRequest},
		{"unknown field", profileLFDIB, `{"name":"x","description":"y"}`, http.StatusBadRequest},
		{"not json", profileLFDIB, `nope`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := putProfile(h.HandlePutDeviceProfile(), c.lfdi, c.body)
			if w.Code != c.code {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, c.code, w.Body.String())
			}
			if _, err := profiles.Get(context.Background(), profileLFDIB); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a refused write stored a profile: err = %v", err)
			}
		})
	}
}

func TestPutDeviceProfileKeepsOwnNameOnRewrite(t *testing.T) {
	h := &handler.AdminDeviceProfileHandler{Profiles: memory.NewDeviceProfileStore()}
	for i, body := range []string{`{"name":"Same","kind":"device"}`, `{"name":"SAME","kind":"aggregator"}`} {
		if w := putProfile(h.HandlePutDeviceProfile(), profileLFDIA, body); w.Code != http.StatusOK {
			t.Fatalf("write %d: status = %d; body = %s", i, w.Code, w.Body.String())
		}
	}
	got, err := h.Profiles.Get(context.Background(), profileLFDIA)
	if err != nil || got.Name != "SAME" || got.Kind != store.DeviceKindAggregator {
		t.Errorf("profile = %+v, %v; want the second write", got, err)
	}
}

func TestPutDeviceProfileStoreFailureAnswers500WithoutText(t *testing.T) {
	h := &handler.AdminDeviceProfileHandler{Profiles: failingProfiles{DeviceProfileStore: memory.NewDeviceProfileStore(), err: errors.New("secret path /var/x")}}
	w := putProfile(h.HandlePutDeviceProfile(), profileLFDIA, `{"name":"x"}`)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "/var/x") {
		t.Errorf("status = %d body = %s; want 500 without the error text", w.Code, w.Body.String())
	}
}

func TestAddEndDeviceRollbackSurvivesCancelledRequest(t *testing.T) {
	devs := memory.NewEndDeviceStore()
	regs := &stubRegs{}
	sfdi, lfdi := freshAddDeviceCert(t)
	body := fmt.Sprintf(`{"sfdi":%q,"lfdi":%q,"name":"Roof","pin":1,"enabled":true}`, sfdi, lfdi)
	ctx, cancel := context.WithCancel(context.Background())
	// The client is gone by the time the profile write fails.
	profiles := cancellingProfiles{cancel: cancel}
	req := httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleAddEndDevice(devs, regs, profiles)(w, req)

	if ctx.Err() == nil {
		t.Fatal("control: the request context was never cancelled")
	}
	if regs.created {
		t.Error("Registration survived because the rollback ran on the cancelled request context")
	}
	if _, err := devs.GetByLFDI(context.Background(), lfdi); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("EndDevice survived: err = %v", err)
	}
}

// cancellingProfiles cancels the request, then fails the Put.
type cancellingProfiles struct {
	store.DeviceProfileStore
	cancel context.CancelFunc
}

func (c cancellingProfiles) Put(context.Context, store.DeviceProfile) error {
	c.cancel()
	return errors.New("client gone")
}

func TestAddEndDeviceFailedAddLeavesNeitherRecordInWiredStores(t *testing.T) {
	// The admin plane is wired with the plain EndDevice store and the plain
	// Registration store, so the rollback must remove both itself.
	devs := memory.NewEndDeviceStore()
	regs := memory.NewRegistrationStore()
	profiles := failingProfiles{DeviceProfileStore: memory.NewDeviceProfileStore(), err: errors.New("disk full")}

	w, lfdi := postAddDevice(t, devs, regs, profiles, `,"name":"Roof"`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if _, err := devs.GetByLFDI(context.Background(), lfdi); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("EndDevice survived: err = %v", err)
	}
	list, err := regs.List(context.Background(), store.ListOptions{Unbounded: true})
	if err != nil || list.All != 0 {
		t.Errorf("registrations after a failed add = %d, %v; want 0", list.All, err)
	}
}

// failingDeleteDevices refuses Delete, as a store that cannot undo a Create.
type failingDeleteDevices struct{ store.EndDeviceStore }

func (failingDeleteDevices) Delete(context.Context, string) error {
	return errors.New("delete refused")
}

func TestAddEndDeviceRegistrationFailureLogsFailedRollback(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	devs := failingDeleteDevices{memory.NewEndDeviceStore()}
	regs := &stubRegs{createErr: errors.New("regs down")}
	w, _ := postAddDevice(t, devs, regs, memory.NewDeviceProfileStore(), ``)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(buf.String(), "rollback end device") {
		t.Errorf("a failed rollback was not logged: %q", buf.String())
	}
}
