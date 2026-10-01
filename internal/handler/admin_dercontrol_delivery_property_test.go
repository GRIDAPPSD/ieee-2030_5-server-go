package handler

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"testing"
)

// bruteIntegrate values each second on its own: per leg, the covering span
// received last (then the later order) wins; then the totals, Average first,
// else the phase sum over every seen phase, Average preferred per phase.
func bruteIntegrate(spans map[leg][]powerSpan, seen map[leg]bool) (wattSeconds float64, covered int64, used []int) {
	lo, hi := int64(1<<62), int64(-1<<62)
	for _, ss := range spans {
		for _, s := range ss {
			lo, hi = min(lo, s.start), max(hi, s.end)
		}
	}
	var phases []uint8
	for l := range seen {
		if l.phase != phaseTotal && !slices.Contains(phases, l.phase) {
			phases = append(phases, l.phase)
		}
	}
	usedSet := map[int]bool{}
	for t := lo; t < hi; t++ {
		winner := map[leg]powerSpan{}
		for l, ss := range spans {
			for _, s := range ss {
				if s.start > t || s.end <= t {
					continue
				}
				w, ok := winner[l]
				if !ok || s.received > w.received || (s.received == w.received && s.order > w.order) {
					winner[l] = s
				}
			}
		}
		var path []powerSpan
		if s, ok := winner[leg{average: true}]; ok {
			path = []powerSpan{s}
		} else if s, ok := winner[leg{}]; ok {
			path = []powerSpan{s}
		} else if len(phases) > 0 {
			for _, p := range phases {
				s, ok := winner[leg{average: true, phase: p}]
				if !ok {
					s, ok = winner[leg{phase: p}]
				}
				if !ok {
					path = nil
					break
				}
				path = append(path, s)
			}
		}
		if len(path) == 0 {
			continue
		}
		covered++
		for _, s := range path {
			wattSeconds += s.watts
			usedSet[s.order] = true
		}
	}
	for o := range usedSet {
		used = append(used, o)
	}
	slices.Sort(used)
	return wattSeconds, covered, used
}

// The edge sweep in integrate must agree with a per-second brute force on
// random spans, including overlapping Average periods on one leg, whose
// resolved pieces meet at the same instant.
func TestIntegrateMatchesPerSecondBruteForce(t *testing.T) {
	legs := []leg{{average: true}, {}, {average: true, phase: phaseA}, {phase: phaseA}, {phase: phaseB}, {average: true, phase: phaseC}}
	r := rand.New(rand.NewPCG(802, 5))
	for c := range 400 {
		spans := map[leg][]powerSpan{}
		seen := map[leg]bool{}
		order := 0
		for _, l := range legs {
			if r.IntN(3) == 0 {
				continue
			}
			seen[l] = true
			for range 1 + r.IntN(5) {
				start := int64(r.IntN(200))
				spans[l] = append(spans[l], powerSpan{
					start:    start,
					end:      start + 1 + int64(r.IntN(80)),
					watts:    float64(r.IntN(2000) - 500),
					received: int64(r.IntN(20)),
					order:    order,
				})
				order++
			}
		}
		if len(spans) == 0 {
			continue
		}
		copyOf := func() map[leg][]powerSpan {
			out := map[leg][]powerSpan{}
			for l, ss := range spans {
				out[l] = slices.Clone(ss)
			}
			return out
		}
		wantWS, wantCov, wantUsed := bruteIntegrate(copyOf(), seen)
		gotWS, gotCov, usedSpans := integrate(copyOf(), seen)
		gotUsed := make([]int, 0, len(usedSpans))
		for _, s := range usedSpans {
			gotUsed = append(gotUsed, s.order)
		}
		slices.SortFunc(gotUsed, cmp.Compare[int])
		if gotCov != wantCov || gotWS != wantWS || !slices.Equal(gotUsed, wantUsed) {
			t.Fatalf("case %d: integrate = (%v Ws, %d s, used %v), brute force = (%v Ws, %d s, used %v); spans %+v seen %v",
				c, gotWS, gotCov, gotUsed, wantWS, wantCov, wantUsed, spans, seen)
		}
	}
}
