package commitment

import "testing"

func TestWindow_Overlaps(t *testing.T) {
	tests := []struct {
		name string
		w    Window
		o    Window
		want bool
	}{
		{"touching at an instant does not overlap", Window{Start: 0, Duration: 10}, Window{Start: 10, Duration: 10}, false},
		{"sharing one second does overlap", Window{Start: 0, Duration: 10}, Window{Start: 9, Duration: 11}, true},
		{"identical windows overlap", Window{Start: 0, Duration: 10}, Window{Start: 0, Duration: 10}, true},
		{"disjoint windows do not overlap", Window{Start: 0, Duration: 5}, Window{Start: 100, Duration: 5}, false},
		{"zero duration on the left overlaps nothing, even sitting inside the other window", Window{Start: 5, Duration: 0}, Window{Start: 0, Duration: 10}, false},
		{"zero duration on the right overlaps nothing", Window{Start: 0, Duration: 10}, Window{Start: 5, Duration: 0}, false},
		{"both zero duration, same start, overlaps nothing", Window{Start: 5, Duration: 0}, Window{Start: 5, Duration: 0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Overlaps(tc.o); got != tc.want {
				t.Errorf("Overlaps(%+v, %+v) = %v, want %v", tc.w, tc.o, got, tc.want)
			}
			// Overlaps must be symmetric.
			if got := tc.o.Overlaps(tc.w); got != tc.want {
				t.Errorf("Overlaps(%+v, %+v) (reversed) = %v, want %v", tc.o, tc.w, got, tc.want)
			}
		})
	}
}

func TestWindow_Within(t *testing.T) {
	tests := []struct {
		name string
		w    Window
		o    Window
		want bool
	}{
		{"one step inside: exact match is within", Window{Start: 0, Duration: 10}, Window{Start: 0, Duration: 10}, true},
		{"one step inside: strictly interior", Window{Start: 2, Duration: 5}, Window{Start: 0, Duration: 10}, true},
		{"one step past: starts one second before the outer window", Window{Start: -1, Duration: 10}, Window{Start: 0, Duration: 10}, false},
		{"one step past: ends one second after the outer window", Window{Start: 0, Duration: 11}, Window{Start: 0, Duration: 10}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Within(tc.o); got != tc.want {
				t.Errorf("Within(%+v, %+v) = %v, want %v", tc.w, tc.o, got, tc.want)
			}
		})
	}
}

func TestWindow_End(t *testing.T) {
	w := Window{Start: 100, Duration: 50}
	if got := w.End(); got != 150 {
		t.Errorf("End() = %d, want 150", got)
	}
}
