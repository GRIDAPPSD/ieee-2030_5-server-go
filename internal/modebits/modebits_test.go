package modebits

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestAppliesOnUnixIsSilent(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if !appliesOn(goos, "unix-silent") {
			t.Errorf("appliesOn(%q) = false; want true", goos)
		}
	}
	if logs.Len() != 0 {
		t.Errorf("unix logged %q; want nothing", logs.String())
	}
}

func TestAppliesOnWindowsSkipsAndLogsOncePerCheck(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for range 3 {
		if appliesOn("windows", "check-a") {
			t.Fatal("appliesOn(windows) = true; want false")
		}
	}
	if n := strings.Count(logs.String(), "check-a skipped on windows"); n != 1 {
		t.Errorf("check-a skip logged %d times; want 1 (log %q)", n, logs.String())
	}
	appliesOn("windows", "check-b")
	if !strings.Contains(logs.String(), "check-b skipped on windows") {
		t.Errorf("check-b has its own line; log %q", logs.String())
	}
}
