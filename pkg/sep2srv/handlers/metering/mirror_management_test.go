package metering_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Every LFDI in this file is exactly 40 hex characters, canonical form:
// memory.EndDeviceManagementStore.Assign refuses anything else.
const (
	mgmtDeviceLFDI   = "1111111111111111111111111111111111111111"
	mgmtManagerLFDI  = "2222222222222222222222222222222222222222"
	mgmtOutsiderLFDI = "3333333333333333333333333333333333333333"
)

// mirrorInstanceMuxWithManagers is mirrorInstanceMux (mirror_putdelete_test.go)
// widened with a management store, for the GET/PUT/DELETE tests below that
// need a manager, not only a creator, admitted. Kept separate from
// mirrorInstanceMux rather than adding a parameter there: every existing
// call site would have to grow a nil argument for a capability only these
// tests use.
func mirrorInstanceMuxWithManagers(
	mupStore *memory.Store[sep2.MirrorUsagePoint],
	mmrStore *memory.ScopedStore[sep2.MirrorMeterReading],
	managers store.EndDeviceManagementReader,
	caller string,
) *http.ServeMux {
	provider := identityProvider(caller)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(mupStore, managers, provider))
	mux.HandleFunc("PUT /mup/{id}", metering.HandlePutMirrorUsagePoint(mupStore, managers, provider, nil))
	mux.HandleFunc("DELETE /mup/{id}", metering.HandleDeleteMirrorUsagePoint(mupStore, mmrStore, managers, provider))
	mux.HandleFunc("POST /mup/{id}", metering.HandlePostMirrorMeterReading(mupStore, mmrStore, managers, provider))
	return mux
}

// erroringManagers is a store.EndDeviceManagementReader whose ManagerOf
// always fails with a non-ErrNotFound error, for proving a management-store
// outage answers 500 (a check that could not complete) rather than 403 (a
// check that completed and refused): mirror.go's mirrorActor returns the
// error rather than folding it into false, and every /mup route must honor
// that rather than reading "err != nil" as "not authorized".
type erroringManagers struct{ err error }

func (e erroringManagers) ManagerOf(context.Context, string) (string, error) {
	return "", e.err
}

func (e erroringManagers) ManagedBy(context.Context, string) ([]string, error) {
	return nil, e.err
}

// managementCase is one row of the table every route below runs: whether the
// caller is admitted, and why.
type managementCase struct {
	name   string
	caller string
	setup  func(t *testing.T, managers store.EndDeviceManagementStore)
	// wantAdmitted is whether this caller is expected to pass the ownership
	// gate; each route below maps that to its own success status (GET 200,
	// PUT 204, DELETE 200, POST reading 201) or to 403 when false.
	wantAdmitted bool
}

// managementCases covers the four shapes #720's acceptance criteria name:
// the device itself, its current manager, an unassigned manager, and a
// manager whose assignment was revoked (Unassign), each proven through the
// real store.EndDeviceManagementStore rather than a nil reader standing in
// for "no delegation to test".
func managementCases() []managementCase {
	return []managementCase{
		{
			name:         "device itself",
			caller:       mgmtDeviceLFDI,
			setup:        func(t *testing.T, managers store.EndDeviceManagementStore) {},
			wantAdmitted: true,
		},
		{
			name:   "current manager",
			caller: mgmtManagerLFDI,
			setup: func(t *testing.T, managers store.EndDeviceManagementStore) {
				t.Helper()
				if err := managers.Assign(context.Background(), mgmtManagerLFDI, mgmtDeviceLFDI); err != nil {
					t.Fatalf("assign manager: %v", err)
				}
			},
			wantAdmitted: true,
		},
		{
			name:         "unassigned manager",
			caller:       mgmtManagerLFDI,
			setup:        func(t *testing.T, managers store.EndDeviceManagementStore) {},
			wantAdmitted: false,
		},
		{
			name:   "revoked manager",
			caller: mgmtManagerLFDI,
			setup: func(t *testing.T, managers store.EndDeviceManagementStore) {
				t.Helper()
				if err := managers.Assign(context.Background(), mgmtManagerLFDI, mgmtDeviceLFDI); err != nil {
					t.Fatalf("assign manager: %v", err)
				}
				if err := managers.Unassign(context.Background(), mgmtDeviceLFDI); err != nil {
					t.Fatalf("unassign manager: %v", err)
				}
			},
			wantAdmitted: false,
		},
		{
			name:         "outsider, never assigned",
			caller:       mgmtOutsiderLFDI,
			setup:        func(t *testing.T, managers store.EndDeviceManagementStore) {},
			wantAdmitted: false,
		},
	}
}

// TestHandleMirrorUsagePoint_ManagementCases is the GET row of the #720
// acceptance criterion "every /mup route admits the device or its current
// manager and refuses an unassigned manager", proven through the real HTTP
// handler over a real store.EndDeviceManagementStore.
func TestHandleMirrorUsagePoint_ManagementCases(t *testing.T) {
	t.Parallel()
	for _, tc := range managementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			managers := memory.NewEndDeviceManagementStore()
			tc.setup(t, managers)
			id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "MGMT_MUP", "management case")

			mux := mirrorInstanceMuxWithManagers(mupStore, nil, managers, tc.caller)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/"+id, nil))

			if tc.wantAdmitted && w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
			}
			if !tc.wantAdmitted && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestHandlePutMirrorUsagePoint_ManagementCases is the PUT row.
func TestHandlePutMirrorUsagePoint_ManagementCases(t *testing.T) {
	t.Parallel()
	for _, tc := range managementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			managers := memory.NewEndDeviceManagementStore()
			tc.setup(t, managers)
			id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "MGMT_MUP", "management case")

			mux := mirrorInstanceMuxWithManagers(mupStore, mmrStore, managers, tc.caller)
			req := httptest.NewRequest(http.MethodPut, "/mup/"+id, bytes.NewReader(mupWireBody("MGMT_MUP", "updated", "")))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if tc.wantAdmitted {
				if w.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want 204; body = %s", w.Code, w.Body.String())
				}
				stored, err := mupStore.Get(context.Background(), id)
				if err != nil {
					t.Fatalf("get stored MirrorUsagePoint: %v", err)
				}
				if stored.Description != "updated" {
					t.Errorf("stored Description = %q, want %q: an admitted PUT must apply", stored.Description, "updated")
				}
			} else if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestHandleDeleteMirrorUsagePoint_ManagementCases is the DELETE row.
func TestHandleDeleteMirrorUsagePoint_ManagementCases(t *testing.T) {
	t.Parallel()
	for _, tc := range managementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			managers := memory.NewEndDeviceManagementStore()
			tc.setup(t, managers)
			id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "MGMT_MUP", "management case")

			mux := mirrorInstanceMuxWithManagers(mupStore, mmrStore, managers, tc.caller)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mup/"+id, nil))

			if tc.wantAdmitted {
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
				}
				if _, err := mupStore.Get(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("record still present after an admitted DELETE (err = %v)", err)
				}
			} else {
				if w.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
				}
				if _, err := mupStore.Get(context.Background(), id); err != nil {
					t.Errorf("record removed by a refused DELETE: %v", err)
				}
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_ManagementCases is the reading-POST row,
// over both mounted paths.
func TestHandlePostMirrorMeterReading_ManagementCases(t *testing.T) {
	t.Parallel()
	for _, tc := range managementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			managers := memory.NewEndDeviceManagementStore()
			tc.setup(t, managers)
			id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "MGMT_MUP", "management case")

			mux := mirrorInstanceMuxWithManagers(mupStore, mmrStore, managers, tc.caller)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup/"+id, bytes.NewReader(readingBody(t, "MGMT_MMR", 42))))

			if tc.wantAdmitted && w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
			}
			if !tc.wantAdmitted && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestHandleMirrorUsagePoint_ManagementStoreErrorIs500 proves a management-
// store outage is reported as 500, not misread as "not authorized" (403):
// the mutant `return false, err` -> `return false, nil` inside the
// mirrorActor would make this test the only one that notices.
func TestHandleMirrorUsagePoint_ManagementStoreErrorIs500(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "MGMT_ERR", "faulty manager lookup")
	managers := erroringManagers{err: errors.New("management store unavailable")}

	mux := mirrorInstanceMuxWithManagers(mupStore, nil, managers, mgmtManagerLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mup/"+id, nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (management store failed, not merely refused); body = %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateMirrorUsagePoint_ManagementStoreErrorIs500 is the create-path
// twin: the device-claim check on POST /mup has its own actFor call site.
func TestHandleCreateMirrorUsagePoint_ManagementStoreErrorIs500(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	managers := erroringManagers{err: errors.New("management store unavailable")}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(mupStore, managers, identityProvider(mgmtManagerLFDI), nil))

	body := mupWireBody("MGMT_ERR_CREATE", "x", mgmtDeviceLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (management store failed, not merely refused); body = %s", w.Code, w.Body.String())
	}
	if count, _ := mupStore.Count(context.Background()); count != 0 {
		t.Errorf("stored count = %d, want 0: an indeterminate check must not store anything", count)
	}
}

// TestHandlePutMirrorUsagePoint_AbsentDeviceLFDIKeepsStoredDeviceNotCaller is
// the mutant killer for mirror.go's PUT device resolution: absent deviceLFDI
// must default to the STORED device, not to the caller. A manager PUTting a
// managed device's mirror, leaving deviceLFDI absent, must not reassign the
// mirror to itself: mirror.go's `resolveMirroredDevice(mup.DeviceLFDI,
// storedMup.DeviceLFDI)` mutated to use lfdi (the caller) instead of
// storedMup.DeviceLFDI would flip DeviceLFDI to the manager's own identity
// here, and every other PUT test uses a caller equal to the stored device,
// which cannot distinguish the two.
func TestHandlePutMirrorUsagePoint_AbsentDeviceLFDIKeepsStoredDeviceNotCaller(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(context.Background(), mgmtManagerLFDI, mgmtDeviceLFDI); err != nil {
		t.Fatalf("assign manager: %v", err)
	}
	id := seedDerivedMirror(t, mupStore, mgmtDeviceLFDI, "MGMT_ABSENT", "original")

	mux := mirrorInstanceMuxWithManagers(mupStore, mmrStore, managers, mgmtManagerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+id, bytes.NewReader(mupWireBody("MGMT_ABSENT", "updated by manager", "")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", w.Code, w.Body.String())
	}
	stored, err := mupStore.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != mgmtDeviceLFDI {
		t.Errorf("stored DeviceLFDI = %q, want %q (the mirrored device, unchanged): a manager's absent-claim PUT must not reassign the mirror to itself", stored.DeviceLFDI, mgmtDeviceLFDI)
	}
	if stored.DeviceLFDI == mgmtManagerLFDI {
		t.Fatalf("stored DeviceLFDI = the manager's own LFDI: the mirror was reassigned")
	}
}

// TestHandlePostMirrorMeterReading_ManagerRepostsSameMRID is the LOW mutant
// killer for the ErrAlreadyExists device re-check on POST /mup: a manager
// re-posting the SAME (device, mRID) pair it already created must take the
// rule (a)(4) overwrite path (204), not be refused as though it collided
// with a different device's record. Every existing re-post test uses a
// caller equal to the device (self); this is the manager case, which is what
// the id's re-derivation from device (not caller) actually has to get right.
func TestHandlePostMirrorMeterReading_ManagerRepostsSameMRID(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(context.Background(), mgmtManagerLFDI, mgmtDeviceLFDI); err != nil {
		t.Fatalf("assign manager: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(mupStore, managers, identityProvider(mgmtManagerLFDI), nil))

	body := mupWireBody("MGMT_REPOST", "first", mgmtDeviceLFDI)
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body = %s", first.Code, first.Body.String())
	}
	firstLoc := first.Header().Get("Location")

	body2 := mupWireBody("MGMT_REPOST", "second", mgmtDeviceLFDI)
	second := httptest.NewRecorder()
	mux.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body2)))
	if second.Code != http.StatusNoContent {
		t.Fatalf("manager re-post of the same (device, mRID): status = %d, want 204 (overwrite, not a collision refusal); body = %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("Location"); got != firstLoc {
		t.Errorf("re-post Location = %q, want %q (same record)", got, firstLoc)
	}

	id := firstLoc[len("/mup/"):]
	stored, err := mupStore.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.Description != "second" {
		t.Errorf("stored Description = %q, want %q (the re-post overwrote)", stored.Description, "second")
	}
	if stored.DeviceLFDI != mgmtDeviceLFDI {
		t.Errorf("stored DeviceLFDI = %q, want %q unchanged", stored.DeviceLFDI, mgmtDeviceLFDI)
	}
}

// TestHandleCreateMirrorUsagePoint_ComponentOrProxiedLFDIRefused pins the
// operator decision (#720 issue, D4 in the design) that a DERComponent or
// proxied LFDI (IEEE 2030.5-2023 lines 7860-7861 and 2692-2693: a value
// "traceable by the proxy or end device to the associated device" rather
// than naming an EndDevice the server itself manages) is refused for now,
// exactly like any other well-formed claim naming a device the caller is
// neither nor manages. There is nothing structurally different about such a
// claim for this server to detect; it is refused by the same generic rule,
// and this test exists so that scope decision has a named regression guard.
func TestHandleCreateMirrorUsagePoint_ComponentOrProxiedLFDIRefused(t *testing.T) {
	t.Parallel()
	const posterLFDI = "5555555555555555555555555555555555555555"
	const componentOrProxiedLFDI = "6666666666666666666666666666666666666666"

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(mupStore, nil, identityProvider(posterLFDI), nil))

	body := mupWireBody("COMPONENT_MUP", "component or proxied", componentOrProxiedLFDI)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (component/proxied LFDI, neither self nor a managed device); body = %s", w.Code, w.Body.String())
	}
	if count, _ := mupStore.Count(context.Background()); count != 0 {
		t.Errorf("stored count = %d, want 0", count)
	}
}
