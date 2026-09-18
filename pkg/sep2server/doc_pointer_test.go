package sep2server

import (
	"os"
	"strings"
	"testing"
)

// TestDocPointsAtSep2Admin is the grep criterion issue #367 asks for:
// doc.go's exclusion list must not go unexplained. A reader who finds "the
// admin dashboard... stay[s] in internal/" needs a pointer to where the
// panel contract actually lives, or the exclusion reads as final rather
// than as "moved, not dropped."
func TestDocPointsAtSep2Admin(t *testing.T) {
	src, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatalf("read doc.go: %v", err)
	}
	if !strings.Contains(string(src), "pkg/sep2admin") {
		t.Fatal("doc.go does not point at pkg/sep2admin")
	}
}
