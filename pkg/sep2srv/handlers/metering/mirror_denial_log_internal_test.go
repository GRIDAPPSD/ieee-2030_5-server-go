package metering

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMirrorDenialClock drives a mirrorDenialLog's clock and timer by hand,
// the same technique assembly/ownership.go's own denial-log tests use
// (fakeDenialClock there), so a suppression window can be closed without a
// test actually waiting mirrorDenialWindow.
type fakeMirrorDenialClock struct {
	mu    sync.Mutex
	now   time.Time
	timer *struct {
		at   time.Time
		f    func()
		done bool
	}
}

func newFakeMirrorDenialClock() *fakeMirrorDenialClock {
	return &fakeMirrorDenialClock{now: time.Unix(1700000000, 0)}
}

func (c *fakeMirrorDenialClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeMirrorDenialClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &struct {
		at   time.Time
		f    func()
		done bool
	}{at: c.now.Add(d), f: f}
	c.timer = tm
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		pending := !tm.done
		tm.done = true
		return pending
	}
}

// Advance moves the clock and runs the timer if it is now due.
func (c *fakeMirrorDenialClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	tm := c.timer
	c.mu.Unlock()
	if tm != nil && !tm.done && !tm.at.After(c.now) {
		tm.done = true
		tm.f()
	}
}

func newTestMirrorDenialLog(clock *fakeMirrorDenialClock) (*mirrorDenialLog, *safeLines) {
	out := &safeLines{}
	d := newMirrorDenialLog(out.logf)
	d.now = clock.Now
	d.afterFunc = clock.AfterFunc
	return d, out
}

// safeLines collects formatted log lines under a mutex: mirrorDenialLog can
// call logf from its own timer goroutine (flush), concurrently with the
// test's own calls to record.
type safeLines struct {
	mu    sync.Mutex
	lines []string
}

func (s *safeLines) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *safeLines) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// TestMirrorDenialLog_PerCallerCap is the direct mechanism proof behind
// TestHandleMirrorUsagePoint_DenialLogIsRateLimitedAndNamesTheCaller
// (mirror_denial_log_test.go): 1000 refusals from one caller write exactly
// mirrorDenialPerCaller lines, and a second caller's first refusal is never
// suppressed by the first caller's budget.
func TestMirrorDenialLog_PerCallerCap(t *testing.T) {
	t.Parallel()
	d, out := newTestMirrorDenialLog(newFakeMirrorDenialClock())

	for i := 0; i < 1000; i++ {
		d.record("NOISY", "not-device-or-manager", "line for NOISY")
	}
	d.record("QUIET", "not-device-or-manager", "line for QUIET")

	noisy, quiet := 0, 0
	for _, line := range out.snapshot() {
		switch line {
		case "line for NOISY":
			noisy++
		case "line for QUIET":
			quiet++
		}
	}
	if noisy != mirrorDenialPerCaller {
		t.Errorf("NOISY wrote %d lines, want %d (mirrorDenialPerCaller)", noisy, mirrorDenialPerCaller)
	}
	if quiet != 1 {
		t.Errorf("QUIET wrote %d lines, want 1: one caller's refusals must not spend another's budget", quiet)
	}
}

// TestMirrorDenialLog_TotalCapAcrossCallers proves the second bound: even
// spread across distinct callers (one line each, well under any caller's own
// budget), the log stops writing at mirrorDenialLimit and reports the rest
// as a suppressed count once the window closes.
func TestMirrorDenialLog_TotalCapAcrossCallers(t *testing.T) {
	t.Parallel()
	clock := newFakeMirrorDenialClock()
	d, out := newTestMirrorDenialLog(clock)

	const callers = mirrorDenialLimit + 37
	for i := 0; i < callers; i++ {
		d.record(fmt.Sprintf("CALLER-%05d", i), "not-device-or-manager", fmt.Sprintf("line %d", i))
	}
	if written := len(out.snapshot()); written != mirrorDenialLimit {
		t.Fatalf("%d distinct callers wrote %d lines before the window closed, want %d (mirrorDenialLimit)", callers, written, mirrorDenialLimit)
	}

	clock.Advance(mirrorDenialWindow)
	var summary string
	for _, line := range out.snapshot() {
		if strings.HasPrefix(line, "mup: suppressed") {
			summary = line
		}
	}
	if summary == "" {
		t.Fatal("no suppression summary line after the window closed")
	}
	wantSuppressed := callers - mirrorDenialLimit
	if !strings.Contains(summary, fmt.Sprintf("suppressed %d", wantSuppressed)) {
		t.Errorf("summary = %q, want it to report %d suppressed", summary, wantSuppressed)
	}
	if !strings.Contains(summary, "not-device-or-manager="+fmt.Sprint(wantSuppressed)) {
		t.Errorf("summary = %q, want the reason breakdown not-device-or-manager=%d", summary, wantSuppressed)
	}
}
