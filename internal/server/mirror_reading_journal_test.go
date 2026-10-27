package server

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const journalDevice = "3E4F45AB31EDFE5B67E343E5E4562E3100000001"

func journalStores(t *testing.T, dataDir string) *Stores {
	t.Helper()
	s, _, err := newRunStores(&config.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func journalMirror(t *testing.T, s *Stores) {
	t.Helper()
	rate := uint32(60)
	mup := sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/m1"}, MRID: "pv-1", DeviceLFDI: journalDevice, RoleFlags: 1 << 3, PostRate: &rate}
	if err := s.MirrorUsagePoints.Create(context.Background(), "m1", mup); err != nil {
		t.Fatal(err)
	}
}

// postAverage POSTs one Average W reading whose timePeriod is the 1000 s
// before now, export 1500 W, as the mirrored device would.
func postAverage(t *testing.T, s *Stores) int {
	t.Helper()
	uom, q, dir := sep2.UomWatts, uint8(2), sep2.FlowDirectionReverse
	v := int64(1500)
	now := time.Now().Unix()
	mmr := sep2.MirrorMeterReading{
		MRID:        "PV-W",
		ReadingType: &sep2.ReadingType{Uom: &uom, DataQualifier: &q, FlowDirection: &dir},
		Reading:     &sep2.Reading{Value: &v, TimePeriod: &sep2.DateTimeInterval{Start: now - 1000, Duration: 1000}},
	}
	body, err := xml.Marshal(&mmr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup/{id}/mr", metering.HandlePostMirrorMeterReading(s.MirrorUsagePoints, s.MirrorMeterReadings, nil, func(ctx context.Context) (string, bool) {
		id, ok := auth.GetIdentity(ctx)
		return id.LFDI, ok
	}))
	req := httptest.NewRequest(http.MethodPost, "/mup/m1/mr", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), auth.IdentityContextKey(), auth.DeviceIdentity{LFDI: journalDevice}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code
}

// totalPoints is the Total line of GET /api/derms/power?window=900&scope=all.
func totalPoints(t *testing.T, s *Stores) [][2]float64 {
	t.Helper()
	h := &handler.AdminFleetHandler{
		Managers:            s.EndDeviceManagers.(*memory.EndDeviceManagementStore),
		MirrorUsagePoints:   s.MirrorUsagePoints,
		MirrorMeterReadings: s.MirrorMeterReadings,
		PowerHistory:        s.PowerHistory,
	}
	w := httptest.NewRecorder()
	handler.HandlePower(h, 0)(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?window=900&scope=all", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("power: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Chart struct {
			Series []struct {
				Name   string
				Points [][2]float64
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, s := range resp.Chart.Series {
		if s.Name == "Total" {
			return s.Points
		}
	}
	return nil
}

// #1206: a reading POSTed before a restart is still read after it, so the
// 15 min chart has a Total point for every 5 s bucket up to the restart.
func TestMirrorMeterReadings_SurviveARestart(t *testing.T) {
	dir := t.TempDir()
	s1 := journalStores(t, dir)
	journalMirror(t, s1)
	if code := postAverage(t, s1); code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201", code)
	}
	before := totalPoints(t, s1)

	s2 := journalStores(t, dir)
	after := totalPoints(t, s2)
	for name, pts := range map[string][][2]float64{"before": before, "after": after} {
		if len(pts) != 180 {
			t.Fatalf("%s the restart: %d Total points, want 180", name, len(pts))
		}
		for _, p := range pts {
			if p[1] != 1500 {
				t.Fatalf("%s the restart: Total %v W at %v, want 1500", name, p[1], p[0])
			}
		}
	}
	if s2.PowerHistory == nil {
		t.Fatal("no power history with a data directory")
	}
}

// Control and the no-data-directory mode: the same POST without a data
// directory leaves no journal and an empty chart after a restart.
func TestMirrorMeterReadings_NoDataDirIsMemoryOnly(t *testing.T) {
	s1, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s1.MirrorMeterReadings.(*memory.ScopedStore[sep2.MirrorMeterReading]); !ok {
		t.Fatalf("reading store without a data directory is %T, want the in-memory store", s1.MirrorMeterReadings)
	}
	journalMirror(t, s1)
	if code := postAverage(t, s1); code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201", code)
	}
	if got := len(totalPoints(t, s1)); got != 180 {
		t.Fatalf("Total points before the restart = %d, want 180", got)
	}
	s2, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	journalMirror(t, s2)
	if got := len(totalPoints(t, s2)); got != 0 {
		t.Errorf("Total points after a restart without a data directory = %d, want 0", got)
	}
}

// A reading whose journal append fails answers 500 and is not stored.
func TestMirrorMeterReadings_FailedJournalWriteIs500(t *testing.T) {
	dir := t.TempDir()
	s := journalStores(t, dir)
	journalMirror(t, s)
	// A directory where the journal file goes makes every append fail.
	if err := os.Remove(filepath.Join(dir, "mirrormeterreadings.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "mirrormeterreadings.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := postAverage(t, s); code != http.StatusInternalServerError {
		t.Fatalf("POST = %d, want 500", code)
	}
	if n, err := s.MirrorMeterReadings.Count(context.Background(), "m1"); err != nil || n != 0 {
		t.Errorf("readings after a failed POST = %d, %v; want 0", n, err)
	}
}

// A cap deletion before a restart stays deleted after the replay, and the
// boot sweep after the replay removes nothing more. Readings of a mirror
// that no longer exists are dropped at boot.
func TestMirrorMeterReadings_RetentionAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s1 := journalStores(t, dir)
	journalMirror(t, s1)
	uom, dirn := sep2.UomWatts, sep2.FlowDirectionReverse
	now := time.Now()
	for i := range 5 {
		v := int64(100 * i)
		id := fmt.Sprintf("%019d", now.Add(time.Duration(i-5)*time.Second).UnixNano())
		r := sep2.MirrorMeterReading{
			Resource:       sep2.Resource{Href: "/mup/m1/mr/" + id},
			MRID:           "PV-W",
			LastUpdateTime: now.Unix() - int64(5-i),
			ReadingType:    &sep2.ReadingType{Uom: &uom, FlowDirection: &dirn},
			Reading:        &sep2.Reading{Value: &v},
		}
		if err := s1.MirrorMeterReadings.Create(ctx, "m1", id, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s1.MirrorMeterReadings.Create(ctx, "gone", "x", sep2.MirrorMeterReading{MRID: "X"}); err != nil {
		t.Fatal(err)
	}
	sweep := func(s *Stores) int {
		t.Helper()
		r := &mirrorretention.Retention{Readings: s.MirrorMeterReadings, Mirrors: s.MirrorUsagePoints, MaxAge: 25 * time.Hour, MaxPerSeries: 3}
		n, err := r.Sweep(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := sweep(s1); n != 2 {
		t.Fatalf("first sweep removed %d, want 2 over the cap of 3", n)
	}
	s2 := journalStores(t, dir)
	page, err := s2.MirrorMeterReadings.List(ctx, "m1", store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || *page.Items[0].Reading.Value != 200 {
		t.Fatalf("after the restart: %d readings, first %v; want the newest 3 from 200 W", len(page.Items), page.Items)
	}
	if n := sweep(s2); n != 0 {
		t.Errorf("sweep after the replay removed %d, want 0", n)
	}
	if n, _ := s2.MirrorMeterReadings.Count(ctx, "gone"); n != 0 {
		t.Errorf("an orphaned mirror's readings survived the restart: %d", n)
	}
}
