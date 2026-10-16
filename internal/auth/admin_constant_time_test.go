package auth

import "testing"

func TestConstantTimeEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "0123456789abcdef", "0123456789abcdef", true},
		{"equal empty", "", "", true},
		{"same length differs", "0123456789abcdef", "0123456789abcdeg", false},
		{"prefix is not equal", "0123456789abcdef", "0123456789abcdef0", false},
		{"longer vs shorter", "x", "0123456789abcdef", false},
		{"empty vs non-empty", "", "x", false},
		{"multibyte equal", "éé", "éé", true},
	}
	for _, tc := range tests {
		if got := ConstantTimeEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: ConstantTimeEqual(%q, %q) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}
