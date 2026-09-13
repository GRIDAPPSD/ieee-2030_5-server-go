package assembly

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// fakeDenialClock drives a denialLog's clock and timers by hand.
type fakeDenialClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeDenialTimer
}

type fakeDenialTimer struct {
	at   time.Time
	f    func()
	done bool // fired or stopped
}

func newFakeDenialClock() *fakeDenialClock {
	return &fakeDenialClock{now: time.Unix(1700000000, 0)}
}

func (c *fakeDenialClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeDenialClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeDenialTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		pending := !tm.done
		tm.done = true
		return pending
	}
}

// Set moves the clock without running timers, as when a timer runs late.
func (c *fakeDenialClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock and runs every timer then due.
func (c *fakeDenialClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	c.run(false)
}

func (c *fakeDenialClock) RunDue() { c.run(false) }

// FireAll runs every pending timer whatever the clock says, as when a timer
// runs early against the injected clock.
func (c *fakeDenialClock) FireAll() { c.run(true) }

// run calls timer functions outside the clock's lock, because they read it.
func (c *fakeDenialClock) run(all bool) {
	c.mu.Lock()
	var due []func()
	for _, tm := range c.timers {
		if !tm.done && (all || !tm.at.After(c.now)) {
			tm.done = true
			due = append(due, tm.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func (c *fakeDenialClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, tm := range c.timers {
		if !tm.done {
			n++
		}
	}
	return n
}

type denialOutput struct {
	mu    sync.Mutex
	lines []string
}

func (o *denialOutput) logf(format string, args ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = append(o.lines, fmt.Sprintf(format, args...))
}

func (o *denialOutput) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.lines...)
}

func (o *denialOutput) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = nil
}

func newTestDenialLog(clock *fakeDenialClock) (*denialLog, *denialOutput) {
	out := &denialOutput{}
	d := newDenialLog(out.logf)
	d.now = clock.Now
	d.afterFunc = clock.AfterFunc
	return d, out
}

func denialRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/edev/"+id, nil)
	req.SetPathValue("id", id)
	return req
}

var denialSummaryPattern = regexp.MustCompile(`^assembly: ownership gate suppressed (\d+) denial log lines in the last (\S+): (.+)$`)

// summaries returns the suppressed counts and intervals of the summary lines,
// and the number of refusal lines.
func summaries(t *testing.T, lines []string) (counts []int, intervals []string, denials int) {
	t.Helper()
	for _, line := range lines {
		switch m := denialSummaryPattern.FindStringSubmatch(line); {
		case m != nil:
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("summary count in %q: %v", line, err)
			}
			counts = append(counts, n)
			intervals = append(intervals, m[2])
		case strings.Contains(line, "ownership gate denied"):
			denials++
		default:
			t.Fatalf("unexpected denial log line %q", line)
		}
	}
	return counts, intervals, denials
}

func TestDenialLog_ReportsSuppressedCountWithoutAnotherRefusal(t *testing.T) {
	t.Parallel()
	clock := newFakeDenialClock()
	d, out := newTestDenialLog(clock)

	for i := 0; i < 10; i++ {
		d.record(denialRequest("1"), ownershipVerdict{caller: "NOISY", reason: reasonNotOwner})
	}
	for i := 0; i < 3; i++ {
		d.record(denialRequest("404"), ownershipVerdict{caller: "NOISY", reason: reasonAbsent})
	}
	if got := len(out.snapshot()); got != denialLogPerCaller {
		t.Fatalf("burst wrote %d lines, want %d", got, denialLogPerCaller)
	}
	if got := clock.Pending(); got != 1 {
		t.Fatalf("a window that suppressed lines armed %d timers, want 1", got)
	}

	clock.Advance(denialLogWindow - time.Second)
	if got := len(out.snapshot()); got != denialLogPerCaller {
		t.Fatalf("the count was reported before the window closed: %q", out.snapshot()[denialLogPerCaller:])
	}

	clock.Advance(time.Second)
	lines := out.snapshot()
	want := "assembly: ownership gate suppressed 8 denial log lines in the last 1m0s: absent=3 not-owner=5"
	if len(lines) != denialLogPerCaller+1 || lines[denialLogPerCaller] != want {
		t.Fatalf("with no later refusal, lines after the burst are %q, want [%q]", lines[denialLogPerCaller:], want)
	}

	clock.Advance(10 * time.Minute)
	if got := len(out.snapshot()); got != denialLogPerCaller+1 || clock.Pending() != 0 {
		t.Errorf("after the report: %d lines and %d timers pending, want %d and 0", got, clock.Pending(), denialLogPerCaller+1)
	}

	quiet, quietOut := newTestDenialLog(clock)
	quiet.record(denialRequest("1"), ownershipVerdict{caller: "QUIET", reason: reasonNotOwner})
	clock.Advance(2 * denialLogWindow)
	if clock.Pending() != 0 || len(quietOut.snapshot()) != 1 {
		t.Errorf("a window that suppressed nothing: %d timers pending, lines %q; want no timer and one line", clock.Pending(), quietOut.snapshot())
	}
}

func TestDenialLog_SummaryNamesTheIntervalItCovers(t *testing.T) {
	t.Parallel()
	burst := func(d *denialLog) {
		for i := 0; i <= denialLogPerCaller; i++ {
			d.record(denialRequest("1"), ownershipVerdict{caller: "NOISY", reason: reasonNotOwner})
		}
	}
	start := newFakeDenialClock().Now()

	t.Run("timer runs late", func(t *testing.T) {
		t.Parallel()
		clock := newFakeDenialClock()
		d, out := newTestDenialLog(clock)
		burst(d)
		clock.Set(start.Add(denialLogWindow + 30*time.Second))
		clock.RunDue()
		if _, intervals, _ := summaries(t, out.snapshot()); len(intervals) != 1 || intervals[0] != "1m30s" {
			t.Errorf("intervals %q, want [1m30s]", intervals)
		}
	})

	t.Run("a later refusal closes the window before the timer runs", func(t *testing.T) {
		t.Parallel()
		clock := newFakeDenialClock()
		d, out := newTestDenialLog(clock)
		burst(d)
		clock.Set(start.Add(time.Hour))
		d.record(denialRequest("2"), ownershipVerdict{caller: "OTHER", reason: reasonNotOwner})
		clock.RunDue()
		clock.FireAll()
		lines := out.snapshot()
		counts, intervals, _ := summaries(t, lines)
		if len(counts) != 1 || counts[0] != 1 || intervals[0] != "1h0m0s" {
			t.Errorf("counts %v intervals %q, want one report of 1 over 1h0m0s", counts, intervals)
		}
		if last := lines[len(lines)-1]; !strings.Contains(last, `caller="OTHER"`) {
			t.Errorf("the refusal that closed the window was not written after the report: %q", lines)
		}
		if clock.Pending() != 0 {
			t.Errorf("%d timers pending, want 0", clock.Pending())
		}
	})

	t.Run("timer runs early against the clock", func(t *testing.T) {
		t.Parallel()
		clock := newFakeDenialClock()
		d, out := newTestDenialLog(clock)
		burst(d)
		clock.Set(start.Add(30 * time.Second))
		clock.FireAll()
		if counts, _, _ := summaries(t, out.snapshot()); len(counts) != 0 || clock.Pending() != 1 {
			t.Fatalf("an early timer reported %v and left %d pending; want no report and one re-armed timer", counts, clock.Pending())
		}
		clock.Advance(30 * time.Second)
		if _, intervals, _ := summaries(t, out.snapshot()); len(intervals) != 1 || intervals[0] != "1m0s" {
			t.Errorf("intervals %q, want [1m0s]", intervals)
		}
	})
}

func TestDenialLog_OneCallerCannotSpendAnothersBudget(t *testing.T) {
	t.Parallel()
	d, out := newTestDenialLog(newFakeDenialClock())

	for i := 0; i < 1000; i++ {
		d.record(denialRequest("1"), ownershipVerdict{caller: "NOISY", reason: reasonNotOwner})
	}
	d.record(denialRequest("2"), ownershipVerdict{caller: "QUIET", reason: reasonNotOwner})

	noisy, quiet := 0, 0
	for _, line := range out.snapshot() {
		switch {
		case strings.Contains(line, `caller="NOISY"`):
			noisy++
		case strings.Contains(line, `caller="QUIET"`):
			quiet++
		}
	}
	if noisy != denialLogPerCaller || quiet != 1 {
		t.Errorf("noisy wrote %d lines and quiet %d, want %d and 1", noisy, quiet, denialLogPerCaller)
	}
}

func TestDenialLog_StateIsBoundedUnderDistinctCallers(t *testing.T) {
	t.Parallel()
	clock := newFakeDenialClock()
	d, out := newTestDenialLog(clock)

	const callers = 10*denialLogLimit + 7
	reasons := []string{reasonNotOwner, reasonAbsent, reasonNotManager}
	for i := 0; i < callers; i++ {
		d.record(denialRequest("1"), ownershipVerdict{caller: fmt.Sprintf("CALLER-%05d", i), reason: reasons[i%len(reasons)]})
	}

	d.mu.Lock()
	tracked, reasonKeys := len(d.perCaller), len(d.suppressed)
	d.mu.Unlock()
	if tracked != denialLogLimit || reasonKeys != len(reasons) {
		t.Errorf("%d distinct callers left %d tracked callers and %d reason counters, want %d and %d", callers, tracked, reasonKeys, denialLogLimit, len(reasons))
	}

	clock.Advance(denialLogWindow)
	counts, _, denials := summaries(t, out.snapshot())
	if denials != denialLogLimit || len(counts) != 1 || counts[0] != callers-denialLogLimit {
		t.Errorf("wrote %d refusal lines and reports %v, want %d lines and one report of %d", denials, counts, denialLogLimit, callers-denialLogLimit)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.perCaller != nil || d.suppressed != nil {
		t.Errorf("a reported window kept its state: %d callers, %d reasons", len(d.perCaller), len(d.suppressed))
	}
}

// TestDenialLog_ConcurrentRefusalsAreEachWrittenOrCountedOnce races refusals
// against the clock and the report timer, and requires every refusal to show
// up exactly once: as its own line or inside one report.
func TestDenialLog_ConcurrentRefusalsAreEachWrittenOrCountedOnce(t *testing.T) {
	t.Parallel()
	clock := newFakeDenialClock()
	d, out := newTestDenialLog(clock)

	// The seed overruns one caller's budget before the race starts, so at
	// least one report exists however the clock and the workers interleave.
	const seed = denialLogPerCaller + 1
	for i := 0; i < seed; i++ {
		d.record(denialRequest("1"), ownershipVerdict{caller: "SEED", reason: reasonNotOwner})
	}

	const workers, perWorker = 8, 2000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				d.record(denialRequest("1"), ownershipVerdict{caller: fmt.Sprintf("C%d", (w+i)%3), reason: reasonNotOwner})
			}
		}()
	}
	done := make(chan struct{})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		for {
			select {
			case <-done:
				return
			default:
				clock.Advance(7 * time.Second)
			}
		}
	}()
	wg.Wait()
	close(done)
	<-ticked
	clock.Advance(2 * denialLogWindow)

	counts, _, denials := summaries(t, out.snapshot())
	reported := 0
	for _, n := range counts {
		reported += n
	}
	if len(counts) == 0 {
		t.Fatalf("control: no window suppressed anything, so the count was never exercised")
	}
	if total := seed + workers*perWorker; denials+reported != total {
		t.Errorf("%d refusals: %d lines plus %d reported in %d reports = %d", total, denials, reported, len(counts), denials+reported)
	}
	if clock.Pending() != 0 {
		t.Errorf("%d timers still pending after every window closed", clock.Pending())
	}
}

func TestOwnershipGate_ReportsSuppressedCountThroughTheGate(t *testing.T) {
	t.Parallel()
	devs := memory.NewEndDeviceStore()
	if err := devs.Create(context.Background(), "1", sep2.EndDevice{LFDI: "OWNER"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := newOwnershipGate(newRecordingMux(), devs, nil, ownerIdentity("CALLER"))
	clock := newFakeDenialClock()
	out := &denialOutput{}
	g.denials.logf, g.denials.now, g.denials.afterFunc = out.logf, clock.Now, clock.AfterFunc

	const probes = 50
	for i := 0; i < probes; i++ {
		if status, ran := serveGated(t, g, "1"); status != http.StatusForbidden || ran {
			t.Fatalf("probe %d: status %d, handler ran %v; want 403 and not run", i, status, ran)
		}
	}
	clock.Advance(denialLogWindow)

	counts, intervals, denials := summaries(t, out.snapshot())
	if denials != denialLogPerCaller || len(counts) != 1 || counts[0] != probes-denialLogPerCaller || intervals[0] != "1m0s" {
		t.Errorf("wrote %d refusal lines and reports %v over %q; want %d lines and one report of %d over 1m0s",
			denials, counts, intervals, denialLogPerCaller, probes-denialLogPerCaller)
	}
}

// denialLogGoroutines counts goroutines with a denialLog method on their stack.
func denialLogGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "(*denialLog)") {
			count++
		}
	}
	return count
}

// waitFor polls cond for up to five seconds.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// TestDenialLog_ReporterLeavesNoGoroutine runs the production timer. Not
// parallel: it reads every goroutine's stack.
func TestDenialLog_ReporterLeavesNoGoroutine(t *testing.T) {
	summary := make(chan string)
	d := newDenialLog(func(format string, args ...any) {
		if line := fmt.Sprintf(format, args...); strings.Contains(line, "suppressed") {
			summary <- line
		}
	})
	d.window = 2 * time.Second

	for i := 0; i <= denialLogPerCaller; i++ {
		d.record(denialRequest("1"), ownershipVerdict{caller: "NOISY", reason: reasonNotOwner})
	}
	if !waitFor(func() bool { return denialLogGoroutines() == 0 }) {
		t.Fatalf("a goroutine runs a denialLog method while the report is only pending")
	}
	if !waitFor(func() bool { return denialLogGoroutines() > 0 }) {
		t.Fatalf("control: the stack scan never saw the reporter blocked in logf, so it cannot see a leak")
	}
	select {
	case line := <-summary:
		if !strings.Contains(line, "suppressed 1 denial log lines") {
			t.Errorf("summary %q, want a count of 1", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no summary within five seconds of a two-second window")
	}
	if !waitFor(func() bool { return denialLogGoroutines() == 0 }) {
		t.Errorf("a goroutine outlived the report")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopTimer != nil || !d.windowEnds.IsZero() {
		t.Errorf("the reported window left a timer or an open window behind")
	}
}
