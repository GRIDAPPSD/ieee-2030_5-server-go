package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
)

// The power history keeps each final 5 s bucket's fleet totals across a
// restart (power route, GET /api/derms/power). Mirror readings are held in
// memory, so without it every bucket before a restart is gone.
//
// A bucket is final maxHoldSeconds after its end, because a reading received
// up to that long after it can still cover it. Final buckets are recorded
// from the same per-bucket rule the route uses (totalBucket) and served back
// in place of the readings. One JSON-lines file per scope holds the records,
// each with a checksum so a torn or altered line is dropped, not served.

const (
	// PowerHistoryStep is the length in seconds of a recorded bucket, the
	// smallest bucket the route draws.
	PowerHistoryStep = 5

	// PowerHistoryInterval is how often the recorder looks for newly final
	// buckets.
	PowerHistoryInterval = PowerHistoryStep * time.Second

	powerHistoryPruneEvery = time.Hour
	powerHistoryFilePrefix = "power-history-"
	powerHistoryFileSuffix = ".jsonl"
	powerHistoryAllScope   = "all"
)

// historyState is what the history knows about one route bucket.
type historyState int

const (
	// historyNone: no record covers the bucket, or only some of its 5 s parts.
	historyNone historyState = iota
	// historyCovered: every 5 s part is recorded and every reporting device
	// covered it.
	historyCovered
	// historyGap: a recorded part had a reporting device missing.
	historyGap
)

// bucketTotal is one bucket's fleet totals under the every-reporting-device
// rule: covering of reporting devices covered it, and the sums are over the
// covering ones. It is the one definition of that rule, shared by the route
// and the recorder.
type bucketTotal struct {
	w, exportWs, importWs float64
	covering, reporting   int
}

// full reports whether every reporting device covered the bucket.
func (t bucketTotal) full() bool { return t.reporting > 0 && t.covering == t.reporting }

// totalBucket totals bucket k of the reporting devices. Each device is
// averaged over its own covered seconds, and its signed watt-seconds are
// scaled to the full bucket the same way, so exportWs plus importWs equals w
// times the bucket length.
func totalBucket(reporting []devicePower, k int, bucketLen float64) bucketTotal {
	t := bucketTotal{reporting: len(reporting)}
	for _, d := range reporting {
		if s := d.sums[k]; s.covered > 0 {
			t.covering++
			t.w += s.wattSeconds / float64(s.covered)
			scale := bucketLen / float64(s.covered)
			t.exportWs += d.signed[k].exportWs * scale
			t.importWs += d.signed[k].importWs * scale
		}
	}
	return t
}

// historyRecord is one final 5 s bucket of one scope. A gap record (Covered
// false) means a reporting device was missing, and carries no totals.
type historyRecord struct {
	At        int64   `json:"t"`
	ExportWs  float64 `json:"x"`
	ImportWs  float64 `json:"i"`
	Reporting int     `json:"n"`
	Covered   bool    `json:"c"`
	Sum       uint32  `json:"k"`
}

func (r historyRecord) checksum() uint32 {
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	return crc32.ChecksumIEEE([]byte(fmt.Sprintf("%d|%s|%s|%d|%t", r.At, f(r.ExportWs), f(r.ImportWs), r.Reporting, r.Covered)))
}

// valid reports whether r could have been written by the recorder.
func (r historyRecord) valid() bool {
	switch {
	case r.At <= 0 || r.At%PowerHistoryStep != 0 || r.Reporting < 1:
		return false
	case math.IsNaN(r.ExportWs) || math.IsInf(r.ExportWs, 0) || math.IsNaN(r.ImportWs) || math.IsInf(r.ImportWs, 0):
		return false
	case r.ExportWs < 0 || r.ImportWs > 0:
		return false
	case !r.Covered && (r.ExportWs != 0 || r.ImportWs != 0):
		return false
	}
	return r.Sum == r.checksum()
}

// historyScope is one scope's records in ascending time order.
type historyScope struct {
	recs  []historyRecord
	dirty bool  // the file may not match recs; rewrite it before appending
	next  int64 // the first bucket start not yet looked at, this process only
}

// PowerHistory records final power buckets under a directory. A nil
// *PowerHistory is the no-data-directory mode and is never used: callers
// check for nil.
type PowerHistory struct {
	dir       string
	retention time.Duration

	// writeMu serializes the recorder's file writes and holds no reader
	// back; mu guards scopes and lastPrune and is never held across I/O.
	writeMu   sync.Mutex
	mu        sync.Mutex
	scopes    map[string]*historyScope
	lastPrune int64
}

// OpenPowerHistory loads the records under dir, creating it if needed, and
// drops everything older than retention before now. A line that does not
// decode, fails its checksum, or is out of order is dropped and counted in
// the log, and the file is rewritten without it, so damage is never served
// and never carried forward. A file whose name is not a scope is left alone.
func OpenPowerHistory(dir string, retention time.Duration, now int64) (*PowerHistory, error) {
	if dir == "" || retention <= 0 {
		return nil, errors.New("power history: needs a directory and a positive retention")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("power history: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("power history: %w", err)
	}
	p := &PowerHistory{dir: dir, retention: retention, scopes: map[string]*historyScope{}, lastPrune: now}
	cutoff := p.cutoff(now)
	for _, e := range entries {
		scope, ok := scopeOfFile(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		recs, dropped, err := readHistoryFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("power history: %w", err)
		}
		kept := recs[sort.Search(len(recs), func(i int) bool { return recs[i].At >= cutoff }):]
		if dropped > 0 {
			log.Printf("admin power history: %s: dropped %d record(s) that failed their checksum, did not decode or were out of order; the file is rewritten without them", logsafe.Str(e.Name()), dropped)
		}
		p.scopes[scope] = &historyScope{recs: slices.Clone(kept)}
		if dropped > 0 || len(kept) != len(recs) {
			if err := p.rewrite(scope); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

func (p *PowerHistory) cutoff(now int64) int64 { return now - int64(p.retention/time.Second) }

func (p *PowerHistory) path(scope string) string {
	return filepath.Join(p.dir, powerHistoryFilePrefix+scope+powerHistoryFileSuffix)
}

// scopeOfFile is the scope a history file name belongs to: "all" or a
// canonical aggregator LFDI. Any other name is not ours.
func scopeOfFile(name string) (string, bool) {
	scope, ok := strings.CutPrefix(name, powerHistoryFilePrefix)
	if !ok {
		return "", false
	}
	if scope, ok = strings.CutSuffix(scope, powerHistoryFileSuffix); !ok {
		return "", false
	}
	if scope == powerHistoryAllScope {
		return scope, true
	}
	if c, ok := sep2lfdi.Canonical(scope); ok && c == scope {
		return scope, true
	}
	return "", false
}

// readHistoryFile returns the valid records of path in ascending order and
// how many lines it dropped. A missing file is empty.
func readHistoryFile(path string) ([]historyRecord, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("read %q: %w", path, err)
	}
	var recs []historyRecord
	dropped := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r historyRecord
		if err := json.Unmarshal(line, &r); err != nil || !r.valid() || (len(recs) > 0 && r.At <= recs[len(recs)-1].At) {
			dropped++
			continue
		}
		recs = append(recs, r)
	}
	return recs, dropped, nil
}

func encodeRecords(recs []historyRecord) ([]byte, error) {
	var buf bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// rewrite replaces scope's file with its records, or removes the file when
// none remain. Only the recorder (writeMu) and open change a scope's records,
// so it reads them without p.mu.
func (p *PowerHistory) rewrite(scope string) error {
	sc := p.scopes[scope]
	if len(sc.recs) == 0 {
		if err := os.Remove(p.path(scope)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("power history: remove %q: %w", p.path(scope), err)
		}
		sc.dirty = false
		return nil
	}
	payload, err := encodeRecords(sc.recs)
	if err != nil {
		return fmt.Errorf("power history: encode %s: %w", scope, err)
	}
	if err := atomicfile.Write(p.path(scope), payload); err != nil {
		return fmt.Errorf("power history: %w", err)
	}
	sc.dirty = false
	return nil
}

// lookup totals the route bucket [start, end) of scope from the records. It
// answers historyCovered only when every 5 s part is recorded and covered, so
// a bucket with a missing part is computed from readings instead.
func (p *PowerHistory) lookup(scope string, start, end int64) (bucketTotal, historyState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sc := p.scopes[scope]
	if sc == nil {
		return bucketTotal{}, historyNone
	}
	i := sort.Search(len(sc.recs), func(i int) bool { return sc.recs[i].At >= start })
	var t bucketTotal
	parts, gap := 0, false
	for ; i < len(sc.recs) && sc.recs[i].At < end; i++ {
		r := sc.recs[i]
		parts++
		if !r.Covered {
			gap = true
			continue
		}
		t.exportWs += r.ExportWs
		t.importWs += r.ImportWs
		t.covering, t.reporting = r.Reporting, r.Reporting
	}
	switch {
	case gap:
		return bucketTotal{}, historyGap
	case parts == int((end-start)/PowerHistoryStep):
		t.w = (t.exportWs + t.importWs) / float64(end-start)
		return t, historyCovered
	}
	return bucketTotal{}, historyNone
}

// finalEnd is the end of the newest bucket that is final at now.
func finalEnd(now int64) int64 { return (now - maxHoldSeconds) / PowerHistoryStep * PowerHistoryStep }

// view is the route's read of the history at now: a lookup that answers only
// for buckets that are final. Nil without a history.
func (p *PowerHistory) view(scope string, now int64) func(start, end int64) (bucketTotal, historyState) {
	if p == nil {
		return nil
	}
	final := finalEnd(now)
	return func(start, end int64) (bucketTotal, historyState) {
		if end > final {
			return bucketTotal{}, historyNone
		}
		return p.lookup(scope, start, end)
	}
}

// Record writes every bucket that became final since each scope's last
// record, for scope all and for each aggregator's fleet. A device is
// reporting for a bucket when it covered a second of the latestReportedSeconds
// before the batch ends, the same span the latest total uses. A bucket where
// no reporting device covered any second is not recorded, as the route draws
// no point and counts no gap there. A failed write leaves the scope's cursor
// where it was, so the next call retries the same buckets.
func (p *PowerHistory) Record(ctx context.Context, h *AdminFleetHandler, now int64) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	final := finalEnd(now)
	aggregators := canonicalAll(h.Managers.Managers(ctx))
	slices.Sort(aggregators)
	type scopeJob struct {
		name    string
		members []string // nil for scope all
		from    int64
	}
	jobs := []scopeJob{{name: powerHistoryAllScope}}
	for _, agg := range aggregators {
		raw, err := fleetMembers(ctx, h.Managers, agg)
		if err != nil {
			return fmt.Errorf("power history: %w", err)
		}
		jobs = append(jobs, scopeJob{name: agg, members: canonicalAll(raw)})
	}

	p.mu.Lock()
	floor := final - int64(p.retention/time.Second)
	earliest := final
	for i := range jobs {
		jobs[i].from = floor
		if sc := p.scopes[jobs[i].name]; sc != nil && len(sc.recs) > 0 {
			jobs[i].from = max(jobs[i].from, sc.recs[len(sc.recs)-1].At+PowerHistoryStep)
		}
		if sc := p.scopes[jobs[i].name]; sc != nil {
			jobs[i].from = max(jobs[i].from, sc.next)
		}
		earliest = min(earliest, jobs[i].from)
	}
	p.mu.Unlock()
	if earliest >= final {
		return p.pruneIfDue(now)
	}

	mirrors, err := mirrorReadingsFor(ctx, "admin power history", h.MirrorUsagePoints, h.MirrorMeterReadings, func(string) bool { return true })
	if err != nil {
		return fmt.Errorf("power history: %w", errPowerRead)
	}
	byDevice := map[string][]deviceMirror{}
	for _, m := range mirrors {
		byDevice[m.deviceLFDI] = append(byDevice[m.deviceLFDI], m)
	}

	bounds := boundsFor(earliest, final, PowerHistoryStep)
	reportedBounds := []int64{earliest - latestReportedSeconds, final}
	devices := map[string]devicePower{}
	reported := map[string]bool{}
	for lfdi, ms := range byDevice {
		devices[lfdi] = devicePowerOf(lfdi, ms, h.Edition, bounds)
		reported[lfdi] = devicePowerOf(lfdi, ms, h.Edition, reportedBounds).covered > 0
	}

	for _, job := range jobs {
		var reporting []devicePower
		for lfdi, d := range devices {
			if reported[lfdi] && (job.members == nil || slices.Contains(job.members, canonicalLFDI(lfdi))) {
				reporting = append(reporting, d)
			}
		}
		if err := p.appendScope(job.name, job.from, earliest, final, bounds, reporting); err != nil {
			return err
		}
	}
	return p.pruneIfDue(now)
}

// appendScope records the buckets of scope from `from` on, with bounds
// starting at boundsStart, then persists them. The in-memory scope changes
// only after the write succeeds; a pass that records nothing still moves the
// scope's cursor to final, so an idle fleet is not rescanned.
func (p *PowerHistory) appendScope(scope string, from, boundsStart, final int64, bounds []int64, reporting []devicePower) error {
	var added []historyRecord
	if len(reporting) > 0 {
		for k := int((from - boundsStart) / PowerHistoryStep); k+1 < len(bounds); k++ {
			t := totalBucket(reporting, k, PowerHistoryStep)
			if t.covering == 0 {
				continue
			}
			r := historyRecord{At: bounds[k], Reporting: t.reporting, Covered: t.full()}
			if r.Covered {
				r.ExportWs, r.ImportWs = t.exportWs, t.importWs
			}
			r.Sum = r.checksum()
			added = append(added, r)
		}
	}

	p.mu.Lock()
	sc := p.scopes[scope]
	if sc == nil {
		sc = &historyScope{}
		p.scopes[scope] = sc
	}
	dirty := sc.dirty
	p.mu.Unlock()

	if len(added) == 0 {
		p.mu.Lock()
		sc.next = final
		p.mu.Unlock()
		return nil
	}
	if dirty {
		// An earlier write may have left a torn line; replace the file with
		// the records held, then append.
		if err := p.rewrite(scope); err != nil {
			return err
		}
	}
	payload, err := encodeRecords(added)
	if err != nil {
		return fmt.Errorf("power history: encode %s: %w", scope, err)
	}
	if err := appendFile(p.path(scope), payload); err != nil {
		p.mu.Lock()
		sc.dirty = true
		p.mu.Unlock()
		return fmt.Errorf("power history: %w", err)
	}
	p.mu.Lock()
	sc.recs = append(sc.recs, added...)
	sc.next = final
	p.mu.Unlock()
	return nil
}

func appendFile(path string, payload []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return fmt.Errorf("append %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	return nil
}

// pruneIfDue removes records older than the retention, in memory and in the
// files, once per powerHistoryPruneEvery. Between prunes a file can hold up to
// that much beyond the bound; memory is trimmed on every prune only, so a
// lookup can still serve a record that is due to go until then.
func (p *PowerHistory) pruneIfDue(now int64) error {
	p.mu.Lock()
	if now-p.lastPrune < int64(powerHistoryPruneEvery/time.Second) {
		p.mu.Unlock()
		return nil
	}
	cutoff := p.cutoff(now)
	var changed []string
	for name, sc := range p.scopes {
		i := sort.Search(len(sc.recs), func(i int) bool { return sc.recs[i].At >= cutoff })
		if i > 0 {
			sc.recs = slices.Clone(sc.recs[i:])
			changed = append(changed, name)
		}
	}
	p.lastPrune = now
	p.mu.Unlock()

	var errs []error
	for _, name := range changed {
		if err := p.rewrite(name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Start records every PowerHistoryInterval until ctx ends or stop is called,
// and returns the stop. A failed pass is logged and retried at the next tick.
func (p *PowerHistory) Start(ctx context.Context, h *AdminFleetHandler) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(PowerHistoryInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := p.Record(ctx, h, nowUnix()); err != nil && ctx.Err() == nil {
					log.Printf("admin power history: %s", logsafe.Err(err))
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
