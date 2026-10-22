package assembly

import (
	"strings"
	"testing"
)

func TestEdevID(t *testing.T) {
	t.Parallel()
	cases := []struct{ path, want string }{
		{"/edev/7", "7"},
		{"/edev/7/der/1/ders", "7"},
		{"/edev/", ""},
		{"/edev", ""},
		{"/dcap", ""},
		{"/tm", ""},
		{"//edev/7", ""},
		{"/edev/" + strings.Repeat("9", maxLoggedEdevID) + "/x", strings.Repeat("9", maxLoggedEdevID)},
		{"/edev/" + strings.Repeat("9", maxLoggedEdevID+1) + "/x", ""},
	}
	for _, c := range cases {
		if got := edevID(c.path); got != c.want {
			t.Errorf("edevID(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestCapStringKeepsUTF8Whole(t *testing.T) {
	t.Parallel()
	if got := capString("abc", 3); got != "abc" {
		t.Errorf("capString at the limit = %q, want abc", got)
	}
	// "e" + a 2-byte rune (0xC3 0xA9): a cut at 2 would split the rune.
	if got := capString("e\xc3\xa9x", 2); got != "e" {
		t.Errorf("capString split a rune: %q", got)
	}
	if got := capString(strings.Repeat("a", 300), maxLoggedPath); len(got) != maxLoggedPath {
		t.Errorf("capString length = %d, want %d", len(got), maxLoggedPath)
	}
}
