package enddevice_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	mgrLFDI     = "AA00000000000000000000000000000000000010"
	managedX    = "CA0000000000000000000000000000000000000B"
	managedXLow = "ca0000000000000000000000000000000000000b"
	// managedXMixed differs from managedX in the case of one letter only.
	managedXMixed = "cA0000000000000000000000000000000000000B"
)

type ctxLFDI struct{}

// identityFromCtx lets one handler answer for several callers.
func identityFromCtx() coreedev.IdentityFunc {
	return func(ctx context.Context) (string, string, bool) {
		l, _ := ctx.Value(ctxLFDI{}).(string)
		return l, "111111111116", l != ""
	}
}

func postAs(h http.Handler, caller, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/edev", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxLFDI{}, caller))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func lfdiBody(lfdi string) string {
	return `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><lFDI>` + lfdi + `</lFDI></EndDevice>`
}

func countDevices(t *testing.T, s store.EndDeviceStore) int {
	t.Helper()
	res, err := s.List(context.Background(), store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(res.Items)
}

func pairedManagers(t *testing.T, managed ...string) *memory.EndDeviceManagementStore {
	t.Helper()
	m := memory.NewEndDeviceManagementStore()
	for _, x := range managed {
		if err := m.Assign(context.Background(), mgrLFDI, strings.ToUpper(x)); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// slowSFDIStore widens the window between the SFDI lookup and the Create.
type slowSFDIStore struct{ *memory.EndDeviceStore }

func (s slowSFDIStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	dev, err := s.EndDeviceStore.GetBySFDI(ctx, sfdi)
	time.Sleep(5 * time.Millisecond)
	return dev, err
}

func TestCreateEndDevice_ConcurrentCollidingSFDIsMintOneRecord(t *testing.T) {
	t.Parallel()
	const a = "A100000000000000000000000000000000000001"
	const b = "B200000000000000000000000000000000000002"
	for round := 0; round < 10; round++ {
		inner := memory.NewEndDeviceStore()
		h := coreedev.HandleCreateEndDevice(slowSFDIStore{inner}, memory.NewEndDeviceIndex(), identityFromCtx(), sfdiFirst8)

		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for i, caller := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = postAs(h, caller, "").Code
			}()
		}
		close(start)
		wg.Wait()

		created, conflicts := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				conflicts++
			}
		}
		if created != 1 || conflicts != 1 {
			t.Fatalf("round %d: codes %v, want one 201 and one 409", round, codes)
		}
		if n := countDevices(t, inner); n != 1 {
			t.Fatalf("round %d: %d records hold the colliding SFDI, want 1", round, n)
		}
	}
}

func TestCreateEndDevice_ManagerPostReturnsAnExistingRecordWithANonDerivedSFDI(t *testing.T) {
	t.Parallel()
	s := memory.NewEndDeviceStore()
	existing := sep2.EndDevice{LFDI: managedX, SFDI: "999999999995"}
	existing.Href = "/edev/7"
	if err := s.Create(context.Background(), "7", existing); err != nil {
		t.Fatal(err)
	}
	h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityFromCtx(), sfdiFirst8,
		coreedev.WithManagers(pairedManagers(t, managedX)))

	w := postAs(h, mgrLFDI, lfdiBody(managedX))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got sep2.EndDevice
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Href != "/edev/7" || got.SFDI != "999999999995" || got.LFDI != managedX {
		t.Errorf("returned href %q SFDI %q LFDI %q, want the stored /edev/7 record untouched", got.Href, got.SFDI, got.LFDI)
	}
	if n := countDevices(t, s); n != 1 {
		t.Errorf("%d records, want 1 (no second record for one LFDI)", n)
	}
}

func TestCreateEndDevice_ManagerPostRefusesAMixedCaseHolderOfTheLFDI(t *testing.T) {
	t.Parallel()
	for name, held := range map[string]string{"lower": managedXLow, "mixed": managedXMixed} {
		s := memory.NewEndDeviceStore()
		existing := sep2.EndDevice{LFDI: held, SFDI: "999999999995"}
		existing.Href = "/edev/7"
		if err := s.Create(context.Background(), "7", existing); err != nil {
			t.Fatal(err)
		}
		h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityFromCtx(), sfdiFirst8,
			coreedev.WithManagers(pairedManagers(t, managedX)))

		w := postAs(h, mgrLFDI, lfdiBody(managedX))
		if w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != "device LFDI conflict" {
			t.Errorf("%s: status %d body %q, want 409 device LFDI conflict", name, w.Code, w.Body.String())
		}
		if n := countDevices(t, s); n != 1 {
			t.Errorf("%s: %d records, want 1", name, n)
		}
	}
}

func TestCreateEndDevice_SelfRegistrationToleratesWhitespaceAroundItsLFDI(t *testing.T) {
	t.Parallel()
	const self = "00CD0000000000000000000000000000000000EF"
	for name, body := range map[string]string{
		"padded":          lfdiBody(" \n" + self + "\t "),
		"short and lower": lfdiBody(" " + strings.ToLower(self[2:]) + " "),
		"whitespace only": lfdiBody("   "),
	} {
		s := memory.NewEndDeviceStore()
		h := coreedev.HandleCreateEndDevice(s, memory.NewEndDeviceIndex(), identityFromCtx(), sfdiFirst8)
		w := postAs(h, self, body)
		if w.Code != http.StatusCreated {
			t.Errorf("%s: status %d, want 201; body=%s", name, w.Code, w.Body.String())
			continue
		}
		if got, err := s.GetByLFDI(context.Background(), self); err != nil || got.LFDI != self {
			t.Errorf("%s: no record under the caller's LFDI (%v)", name, err)
		}
	}
}

func TestResolveClaimedLFDI(t *testing.T) {
	t.Parallel()
	const fb = "FALLBACK"
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"", fb, true},
		{" \n\t ", fb, true},
		{" " + managedXLow + "\n", managedX, true},
		{"ca0000000000000000000000000000000000000b"[2:], "00" + managedX[2:], true},
		{"zz", "", false},
		{"ABC", "", false},
	}
	for _, c := range cases {
		got, ok := coreedev.ResolveClaimedLFDI(c.raw, fb)
		if got != c.want || ok != c.ok {
			t.Errorf("ResolveClaimedLFDI(%q) = %q, %v; want %q, %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

type refusal struct{ caller, reason string }

func TestCreateEndDevice_RefusalReasonsAndBodies(t *testing.T) {
	t.Parallel()
	const stranger = "D100000000000000000000000000000000000001"
	cases := []struct {
		name    string
		opts    func(*testing.T) []coreedev.CreateOption
		body    string
		status  int
		reason  string
		message string
	}{
		{"paired manager, option off",
			func(t *testing.T) []coreedev.CreateOption {
				return []coreedev.CreateOption{coreedev.WithManagers(pairedManagers(t, managedX)), coreedev.WithInBandManagementOff()}
			},
			lfdiBody(managedX), 403, "in-band-off", "in-band management is off"},
		{"no pair",
			func(t *testing.T) []coreedev.CreateOption {
				return []coreedev.CreateOption{coreedev.WithManagers(pairedManagers(t, managedX))}
			},
			lfdiBody(stranger), 403, "not-manager", "lFDI is not managed by this certificate"},
		{"no management store wired",
			func(*testing.T) []coreedev.CreateOption { return nil },
			lfdiBody(managedX), 403, "not-manager", "lFDI is not managed by this certificate"},
		{"malformed lFDI",
			func(t *testing.T) []coreedev.CreateOption {
				return []coreedev.CreateOption{coreedev.WithManagers(pairedManagers(t, managedX))}
			},
			lfdiBody("not-hex"), 400, "invalid-lfdi", "invalid lFDI"},
		{"wrong sFDI",
			func(t *testing.T) []coreedev.CreateOption {
				return []coreedev.CreateOption{coreedev.WithManagers(pairedManagers(t, managedX))}
			},
			`<EndDevice xmlns="urn:ieee:std:2030.5:ns"><lFDI>` + managedX + `</lFDI><sFDI>111111111116</sFDI></EndDevice>`,
			400, "sfdi-mismatch", "sFDI does not match the lFDI"},
	}
	for _, c := range cases {
		var mu sync.Mutex
		var got []refusal
		opts := append(c.opts(t), coreedev.WithRefusalLog(func(_ *http.Request, caller, reason string) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, refusal{caller, reason})
		}))
		h := coreedev.HandleCreateEndDevice(memory.NewEndDeviceStore(), memory.NewEndDeviceIndex(), identityFromCtx(), sfdiFirst8, opts...)
		w := postAs(h, mgrLFDI, c.body)
		if w.Code != c.status || strings.TrimSpace(w.Body.String()) != c.message {
			t.Errorf("%s: status %d body %q, want %d %q", c.name, w.Code, w.Body.String(), c.status, c.message)
		}
		if len(got) != 1 || got[0].reason != c.reason || got[0].caller != mgrLFDI {
			t.Errorf("%s: refusal log %+v, want one entry for %s with reason %s", c.name, got, mgrLFDI, c.reason)
		}
	}
}
