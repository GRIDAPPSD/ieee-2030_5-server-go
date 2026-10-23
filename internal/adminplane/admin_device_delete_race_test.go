package adminplane

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #1120: a program writer checks its device before taking the program lock,
// so a delete that holds the lock when the writer arrives can remove the
// device first. These tests drive the real assign and device-program create
// through the router, with the delete parked inside its locked window until
// the writer has passed its first check and is waiting on the lock.

type deleteWindow struct {
	inDelete chan struct{}
	once     sync.Once
}

// racingDelete returns an EndDeviceDelete that signals the window is open,
// waits for the writer to queue on the lock, then removes the device.
func racingDelete(stores *Stores, deviceID string) (http.HandlerFunc, *deleteWindow) {
	win := &deleteWindow{inDelete: make(chan struct{})}
	return func(w http.ResponseWriter, r *http.Request) {
		win.once.Do(func() { close(win.inDelete) })
		time.Sleep(300 * time.Millisecond)
		if err := stores.EndDevices.Delete(r.Context(), deviceID); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}, win
}

// writeDuringDelete sends the delete, and the writer once the delete's window
// is open, and returns both statuses.
func writeDuringDelete(t *testing.T, h http.Handler, win *deleteWindow, path, body string) (deleteStatus, writerStatus int) {
	t.Helper()
	writer := make(chan int, 1)
	go func() {
		select {
		case <-win.inDelete:
		case <-time.After(5 * time.Second):
			writer <- -1
			return
		}
		writer <- post(h, path, body).Code
	}()
	del := post(h, "/api/devices/9", "", withMethod(http.MethodDelete))
	return del.Code, <-writer
}

func TestDeviceDelete_WritersRecheckTheDeviceUnderTheProgramLock(t *testing.T) {
	cases := []struct {
		name, path, body string
		setup            func(t *testing.T, stores *Stores, h http.Handler)
		assertNoWrite    func(t *testing.T, stores *Stores)
	}{
		{
			name: "assign an FSA that carries a live control",
			path: "/api/devices/9/fsa-assignment", body: `{"fsaHref":"/api/fsas/fsa-T"}`,
			setup: func(t *testing.T, stores *Stores, h http.Handler) {
				fsaControlSetup(t, h)
				tmpl := stores.AdminFSAs.Templates(context.Background(), "fsa-T")
				if len(tmpl) != 1 {
					t.Fatalf("templates on fsa-T = %d, want 1", len(tmpl))
				}
				if r := post(h, "/api/fsas/fsa-T/der-programs/"+tmpl[0].ID+"/controls", fsaControlCreateBody()); r.Code != http.StatusCreated {
					t.Fatalf("live control create = %d %s", r.Code, r.Body)
				}
			},
			assertNoWrite: func(t *testing.T, stores *Stores) {
				ctx := context.Background()
				if _, err := stores.FSAs.Get(ctx, "9", "fsa-T"); !isNotFound(err) {
					t.Errorf("fsa-T on deleted device 9: err = %v, want ErrNotFound", err)
				}
				if progs, err := stores.DERPrograms.List(ctx, "9", store.ListOptions{Unbounded: true}); err != nil || len(progs.Items) != 0 {
					t.Errorf("programs on deleted device 9 = %d, %v, want 0", len(progs.Items), err)
				}
			},
		},
		{
			name: "create a device program",
			path: "/api/devices/9/fsas/fsa-A/der-programs", body: `{"description":"d","primacy":1,"defaultControl":{"type":"connect"}}`,
			setup: func(t *testing.T, stores *Stores, _ http.Handler) {
				if err := stores.FSAs.Create(context.Background(), "9", "fsa-A", sep2.FunctionSetAssignments{MRID: "fsa-A"}); err != nil {
					t.Fatal(err)
				}
			},
			assertNoWrite: func(t *testing.T, stores *Stores) {
				if progs, err := stores.DERPrograms.List(context.Background(), "9", store.ListOptions{Unbounded: true}); err != nil || len(progs.Items) != 0 {
					t.Errorf("programs on deleted device 9 = %d, %v, want 0", len(progs.Items), err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stores := fsaControlStores(t)
			if err := stores.EndDevices.Create(context.Background(), "9", sep2.EndDevice{LFDI: "0000000000000000000000000000000000000009", SFDI: "9"}); err != nil {
				t.Fatal(err)
			}
			del, win := racingDelete(stores, "9")
			stores.EndDeviceDelete = del
			h := buildActions(t, Config{Stores: stores, ControlWrites: true})
			tc.setup(t, stores, h)

			delStatus, writerStatus := writeDuringDelete(t, h, win, tc.path, tc.body)
			if delStatus != http.StatusNoContent {
				t.Fatalf("delete = %d, want 204", delStatus)
			}
			if writerStatus != http.StatusNotFound {
				t.Errorf("%s after the device was deleted = %d, want 404", tc.name, writerStatus)
			}
			tc.assertNoWrite(t, stores)
		})
	}
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
