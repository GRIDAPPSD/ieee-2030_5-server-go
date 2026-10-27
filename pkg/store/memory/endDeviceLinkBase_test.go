package memory_test

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// linkDecoratorCase describes one EndDevice link decorator for the tests that
// pin what the shared base guarantees all of them.
type linkDecoratorCase struct {
	name string
	// build decorates inner.
	build func(inner store.EndDeviceStore) store.EndDeviceStore
	// plant installs a client-style link for key on d.
	plant func(d *sep2.EndDevice, key string)
	// linked reports the owned links on d as hrefs ("" for an absent link).
	linked func(d sep2.EndDevice) []string
	// want is the hrefs a device stored under "7" must be served with.
	want []string
	// logName is the link name the strip log line must carry.
	logName string
}

func linkDecoratorCases() []linkDecoratorCase {
	href := func(l *sep2.ListLink) string {
		if l == nil {
			return ""
		}
		return l.Href
	}
	return []linkDecoratorCase{
		{
			name: "LogEvent",
			build: func(inner store.EndDeviceStore) store.EndDeviceStore {
				return memory.NewLogEventLinkedEndDeviceStore(inner, memory.NewScopedStore[sep2.LogEvent]())
			},
			plant:   func(d *sep2.EndDevice, key string) { d.LogEventListLink = &sep2.ListLink{Href: "/elsewhere/" + key} },
			linked:  func(d sep2.EndDevice) []string { return []string{href(d.LogEventListLink)} },
			want:    []string{"/edev/7/lel"},
			logName: "LogEventListLink",
		},
		{
			name: "FlowReservation",
			build: func(inner store.EndDeviceStore) store.EndDeviceStore {
				reqs, resps := newFlowReservationScopedStores()
				return memory.NewFlowReservationLinkedEndDeviceStore(inner, reqs, resps)
			},
			plant: func(d *sep2.EndDevice, key string) {
				d.FlowReservationRequestListLink = &sep2.ListLink{Href: "/elsewhere/" + key}
				d.FlowReservationResponseListLink = &sep2.ListLink{Href: "/elsewhere/" + key}
			},
			linked: func(d sep2.EndDevice) []string {
				return []string{href(d.FlowReservationRequestListLink), href(d.FlowReservationResponseListLink)}
			},
			want:    []string{"/edev/7/frq", "/edev/7/frp"},
			logName: "FlowReservationRequestListLink",
		},
	}
}

// Every link decorator discards a client-supplied link on every read path and
// stores none of it, and stripping a link from a device with no recoverable key
// is logged in the same way for all of them. The three used to disagree: the
// flow reservation decorator logged and the others stripped silently.
func TestLinkedEndDevices_OneStripBehaviorForEveryDecorator(t *testing.T) {
	for _, tc := range linkDecoratorCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			inner := memory.NewEndDeviceStore()
			s := tc.build(inner)

			dev := sep2.EndDevice{SFDI: "1", LFDI: "AA"}
			dev.Href = "/edev/7"
			tc.plant(&dev, "7")
			if err := s.Create(ctx, "7", dev); err != nil {
				t.Fatalf("Create: %v", err)
			}

			// The client's link never reaches the stored record.
			for i, got := range tc.linked(mustGet(t, inner, "7")) {
				if got != tc.want[i] {
					t.Errorf("stored link %d = %q, want the derived %q", i, got, tc.want[i])
				}
			}
			// And no read path serves it back.
			reads := map[string]func() (sep2.EndDevice, error){
				"Get":       func() (sep2.EndDevice, error) { return s.Get(ctx, "7") },
				"GetBySFDI": func() (sep2.EndDevice, error) { return s.GetBySFDI(ctx, "1") },
				"GetByLFDI": func() (sep2.EndDevice, error) { return s.GetByLFDI(ctx, "AA") },
				"List": func() (sep2.EndDevice, error) {
					r, err := s.List(ctx, store.ListOptions{Unbounded: true})
					if err != nil || len(r.Items) != 1 {
						return sep2.EndDevice{}, err
					}
					return r.Items[0], nil
				},
			}
			for name, read := range reads {
				got, err := read()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				for i, l := range tc.linked(got) {
					if l != tc.want[i] {
						t.Errorf("%s link %d = %q, want %q", name, i, l, tc.want[i])
					}
				}
			}

			// A device stored off the /edev/{key} scheme is served without the
			// links, and the discard is logged.
			bad := sep2.EndDevice{SFDI: "2", LFDI: "BB"}
			bad.Href = "/edev/9/extra"
			tc.plant(&bad, "9")
			if err := inner.Create(ctx, "9", bad); err != nil {
				t.Fatalf("seed malformed: %v", err)
			}
			var buf bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(prev)
			got, err := s.GetBySFDI(ctx, "2")
			if err != nil {
				t.Fatalf("GetBySFDI malformed: %v", err)
			}
			for i, l := range tc.linked(got) {
				if l != "" {
					t.Errorf("malformed href link %d = %q, want it stripped", i, l)
				}
			}
			if out := buf.String(); !strings.Contains(out, tc.logName) || !strings.Contains(out, "/edev/9/extra") {
				t.Errorf("strip log = %q, want a line naming %s and the href", out, tc.logName)
			}
		})
	}
}

func mustGet(t *testing.T, s store.EndDeviceStore, id string) sep2.EndDevice {
	t.Helper()
	d, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get %q: %v", id, err)
	}
	return d
}
