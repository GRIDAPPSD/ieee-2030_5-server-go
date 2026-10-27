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
// canonical aggregator LFDI or the Direct group. Any other name is not ours.
func scopeOfFile(name string) (string, bool) {
	scope, ok := strings.CutPrefix(name, powerHistoryFilePrefix)
	if !ok {
		return "", false
	}
	if scope, ok = strings.CutSuffix(scope, powerHistoryFileSuffix); !ok {
		return "", false
	}
	if scope == powerHistoryAllScope || scope == powerDirectKey {
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

// state is the bucket's standing under the every-reporting-device rule: none
// without a reporting device, covered when every one covered it, else gap. It
// is the one place that rule maps to a state.
func (t bucketTotal) state() historyState {
	switch {
	case t.reporting == 0:
		return historyNone
	case t.full():
		return historyCovered
	}
	return historyGap
}

// part is one 5 s bucket of one scope as the route and the recorder both see
// it. Reporting is zero when no device was reporting; covered says every
// reporting device covered it, and only then are the totals set.
type part struct {
	exportWs, importWs float64
	reporting          int
	covered            bool
}

// partOf is the one conversion from a 5 s total to a part, used to record a
// bucket and to draw a live one.
func partOf(t bucketTotal) part {
	pt := part{reporting: t.reporting, covered: t.state() == historyCovered}
	if pt.covered {
		pt.exportWs, pt.importWs = t.exportWs, t.importWs
	}
	return pt
}

// foldParts totals the 5 s parts of one route bucket from the parts every
// reporting device covered, scaled by parts over covered parts so a bucket
// with an uncovered part still draws; the average power is that of the
// covered parts, so Discharged plus Charged still equals Total times the
// bucket length. It answers historyGap when parts were reporting but none was
// covered, and historyNone when no part had a reporting device.
func foldParts(parts []part) (bucketTotal, historyState) {
	var t bucketTotal
	known, covered := 0, 0
	for _, pt := range parts {
		if pt.reporting == 0 {
			continue
		}
		known++
		if !pt.covered {
			continue
		}
		covered++
		t.exportWs += pt.exportWs
		t.importWs += pt.importWs
		t.covering, t.reporting = pt.reporting, pt.reporting
	}
	switch {
	case known == 0:
		return bucketTotal{}, historyNone
	case covered == 0:
		return bucketTotal{}, historyGap
	}
	t.w = (t.exportWs + t.importWs) / float64(covered*PowerHistoryStep)
	scale := float64(len(parts)) / float64(covered)
	t.exportWs *= scale
	t.importWs *= scale
	return t, historyCovered
}

// fineLead is how many 5 s buckets before a bucket a device may have covered a
// second and still be reporting in it.
const fineLead = latestReportedSeconds / PowerHistoryStep

// fineGrid is the 5 s grid from start to end, preceded by the fineLead
// buckets that decide who is reporting in the first one.
func fineGrid(start, end int64) []int64 {
	return boundsFor(reportedLead(start), end, PowerHistoryStep)
}

// fineTotals is the totals of every 5 s bucket after the lead of a grid the
// devices were integrated over (fineGrid). A device is reporting in a bucket
// when it covered a second of the latestReportedSeconds ending at that
// bucket's end, so who reports depends on neither the chart window nor how
// many buckets one recording pass covers. buckets is the grid's bucket count.
func fineTotals(devices []devicePower, buckets int) []bucketTotal {
	cum := make([][]int, len(devices))
	for i, d := range devices {
		c := make([]int, buckets+1)
		for j := 0; j < buckets; j++ {
			c[j+1] = c[j]
			if d.sums[j].covered > 0 {
				c[j+1]++
			}
		}
		cum[i] = c
	}
	out := make([]bucketTotal, 0, buckets-fineLead)
	var reporting []devicePower
	for j := fineLead; j < buckets; j++ {
		reporting = reporting[:0]
		for i, d := range devices {
			if cum[i][j+1]-cum[i][j+1-fineLead] > 0 {
				reporting = append(reporting, d)
			}
		}
		out = append(out, totalBucket(reporting, j, PowerHistoryStep))
	}
	return out
}

// liveParts is the 5 s parts of the route bucket [start, end) from fineTotals,
// whose first entry is the 5 s bucket starting at from.
func liveParts(totals []bucketTotal, from int64) func(start, end int64) []part {
	return func(start, end int64) []part {
		i := int((start - from) / PowerHistoryStep)
		parts := make([]part, (end-start)/PowerHistoryStep)
		for k := range parts {
			parts[k] = partOf(totals[i+k])
		}
		return parts
	}
}

// fineSource answers the route's buckets from fineTotals, whose first entry is
// the 5 s bucket starting at from.
func fineSource(totals []bucketTotal, from int64) func(start, end int64) (bucketTotal, historyState) {
	parts := liveParts(totals, from)
	return func(start, end int64) (bucketTotal, historyState) {
		return foldParts(parts(start, end))
	}
}

// recordParts is the recorded 5 s parts of the route bucket [start, end) of
// scope; a part with no record has no reporting device.
func (p *PowerHistory) recordParts(scope string, start, end int64) []part {
	p.mu.Lock()
	defer p.mu.Unlock()
	parts := make([]part, (end-start)/PowerHistoryStep)
	sc := p.scopes[scope]
	if sc == nil {
		return parts
	}
	i := sort.Search(len(sc.recs), func(i int) bool { return sc.recs[i].At >= start })
	for ; i < len(sc.recs) && sc.recs[i].At < end; i++ {
		r := sc.recs[i]
		parts[(r.At-start)/PowerHistoryStep] = part{exportWs: r.ExportWs, importWs: r.ImportWs, reporting: r.Reporting, covered: r.Covered}
	}
	return parts
}

// lookup totals the route bucket [start, end) of scope from the records, by
// the same fold as a live bucket.
func (p *PowerHistory) lookup(scope string, start, end int64) (bucketTotal, historyState) {
	return foldParts(p.recordParts(scope, start, end))
}

// holds reports whether any record is held for scope. False on a nil history.
func (p *PowerHistory) holds(scope string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	sc := p.scopes[scope]
	return sc != nil && len(sc.recs) > 0
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

// partsView is view for the 5 s parts: nil without a history, and a nil result
// for a bucket that is not final.
func (p *PowerHistory) partsView(scope string, now int64) func(start, end int64) []part {
	if p == nil {
		return nil
	}
	final := finalEnd(now)
	return func(start, end int64) []part {
		if end > final {
			return nil
		}
		return p.recordParts(scope, start, end)
	}
}

// powerHistoryChunk bounds how many seconds one pass integrates at a time, so
// the first pass over a long retention does not hold it all in memory.
const powerHistoryChunk = 2 * 3600

// Record writes every bucket that became final since each scope's last
// record, for scope all, for each aggregator's fleet and for the devices no
// aggregator manages. Who is reporting in a
// bucket is decided by fineTotals, the rule the route uses. A bucket with no
// reporting device is not recorded; one with reporting devices that none
// covered is recorded as a gap. A failed write leaves
// the scope's cursor where it was, so the next call retries the same buckets.
func (p *PowerHistory) Record(ctx context.Context, h *AdminFleetHandler, now int64) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	final := finalEnd(now)
	type scopeJob struct {
		name string
		has  func(canonical string) bool
		from int64
	}
	jobs := []scopeJob{{name: powerHistoryAllScope, has: func(string) bool { return true }}}
	groups, err := energyGroupsOf(ctx, h, sortedAggregators(ctx, h))
	if err != nil {
		return fmt.Errorf("power history: %w", err)
	}
	for _, g := range groups {
		jobs = append(jobs, scopeJob{name: g.key, has: g.has})
	}

	p.mu.Lock()
	floor := final - int64(p.retention/time.Second)
	earliest := final
	for i := range jobs {
		jobs[i].from = floor
		if sc := p.scopes[jobs[i].name]; sc != nil {
			if len(sc.recs) > 0 {
				jobs[i].from = max(jobs[i].from, sc.recs[len(sc.recs)-1].At+PowerHistoryStep)
			}
			jobs[i].from = max(jobs[i].from, sc.next)
		}
		earliest = min(earliest, jobs[i].from)
	}
	p.mu.Unlock()
	if earliest >= final {
		return p.pruneIfDue(now)
	}

	byDevice, err := mirrorsByDevice(ctx, h, "admin power history", func(string) bool { return true })
	if err != nil {
		return fmt.Errorf("power history: %w", err)
	}
	for start := earliest; start < final; {
		end := min(final, start+powerHistoryChunk)
		grid := fineGrid(start, end)
		devices := make([]devicePower, 0, len(byDevice))
		for lfdi, ms := range byDevice {
			devices = append(devices, devicePowerOf(lfdi, ms, h.Edition, grid))
		}
		for i, job := range jobs {
			if job.from >= end {
				continue
			}
			scoped := filterDevices(devices, job.has)
			from := max(job.from, start)
			totals := fineTotals(scoped, len(grid)-1)[(from-start)/PowerHistoryStep:]
			parts := make([]part, len(totals))
			for k, t := range totals {
				parts[k] = partOf(t)
			}
			if err := p.appendScope(job.name, from, end, parts); err != nil {
				return err
			}
			jobs[i].from = end
		}
		start = end
	}
	return p.pruneIfDue(now)
}

// appendScope records the present parts of scope, the first starting at from,
// then persists them. The in-memory scope changes only after the write
// succeeds; a pass that records nothing still moves the scope's cursor to
// next, so an idle fleet is not rescanned.
func (p *PowerHistory) appendScope(scope string, from, next int64, parts []part) error {
	var added []historyRecord
	for k, pt := range parts {
		if pt.reporting == 0 {
			continue
		}
		r := historyRecord{At: from + int64(k)*PowerHistoryStep, ExportWs: pt.exportWs, ImportWs: pt.importWs, Reporting: pt.reporting, Covered: pt.covered}
		r.Sum = r.checksum()
		added = append(added, r)
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
		sc.next = next
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
	sc.next = next
	p.mu.Unlock()
	return nil
}

// appendFile adds payload to the file and syncs it. If the file does not end
// in a newline, a torn or truncated last line, the append starts a new line so
// it cannot join two records.
func appendFile(path string, payload []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	} else if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		if last[0] != '\n' {
			payload = append([]byte{'\n'}, payload...)
		}
	}
	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("append %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", path, err)
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
