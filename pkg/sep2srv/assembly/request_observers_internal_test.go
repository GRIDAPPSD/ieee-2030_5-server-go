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
