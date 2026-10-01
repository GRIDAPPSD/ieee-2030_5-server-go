package extmodtest

import "testing"

// TestAmbientModFlagTakesTheLastValue locks in that ambientModFlag matches
// go's own flag parsing: the last -mod= field wins, not the first.
func TestAmbientModFlagTakesTheLastValue(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=vendor -mod=mod")

	if got := ambientModFlag(t); got != "mod" {
		t.Fatalf("ambientModFlag() = %q, want %q", got, "mod")
	}
}
