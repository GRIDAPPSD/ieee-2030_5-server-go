package store

import "testing"

func TestDeviceHref(t *testing.T) {
	if got, want := DeviceHref("abc123"), "/edev/abc123"; got != want {
		t.Fatalf("DeviceHref = %q, want %q", got, want)
	}
}
