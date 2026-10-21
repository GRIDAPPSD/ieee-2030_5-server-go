package logsafe

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStr_EscapesEachClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"plain text unchanged", "GET /api/x?a=b c", "GET /api/x?a=b c"},
		{"non-ASCII text unchanged", "caf\u00e9 \u4e16\u754c", "caf\u00e9 \u4e16\u754c"},
		{"empty", "", ""},
		{"LF", "a\nb", `a\nb`},
		{"CR", "a\rb", `a\rb`},
		{"CRLF", "a\r\nb", `a\r\nb`},
		{"tab", "a\tb", `a\tb`},
		{"ESC", "a\x1bb", `a\x1bb`},
		{"NUL", "a\x00b", `a\x00b`},
		{"DEL", "a\x7fb", `a\x7fb`},
		{"C1 control NEL", "a\u0085b", `a\u0085b`},
		{"line separator", "a\u2028b", `a\u2028b`},
		{"paragraph separator", "a\u2029b", `a\u2029b`},
		{"bidi override", "a\u202eb", `a\u202eb`},
		{"bidi isolate", "a\u2066b", `a\u2066b`},
		{"left-to-right mark", "a\u200eb", `a\u200eb`},
		{"backslash", `a\b`, `a\\b`},
		{"typed backslash-n differs from a real LF", `a\nb`, `a\\nb`},
		{"invalid UTF-8", "a\xffb", `a\xffb`},
		{"forged log line", "x\n2026/10/21 00:00:00 admin: FORGED", `x\n2026/10/21 00:00:00 admin: FORGED`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Str(tc.in); got != tc.want {
				t.Errorf("Str(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStr_OutputHasNoControlBytes(t *testing.T) {
	t.Parallel()
	var in strings.Builder
	for r := rune(0); r < 0x100; r++ {
		in.WriteRune(r)
	}
	in.WriteString("\u2028\u2029\u202a\u202e\u2066\u2069\xff")
	got := Str(in.String())
	if !utf8.ValidString(got) {
		t.Errorf("output is not valid UTF-8: %q", got)
	}
	for i := 0; i < len(got); i++ {
		if got[i] < 0x20 || got[i] == 0x7f {
			t.Fatalf("output holds raw control byte %#x at %d: %q", got[i], i, got)
		}
	}
	if strings.ContainsAny(got, "\u0085\u2028\u2029\u202e") {
		t.Errorf("output holds a raw unicode control: %q", got)
	}
}

func TestStr_Cap(t *testing.T) {
	t.Parallel()
	t.Run("at the cap is unchanged", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("a", MaxBytes)
		if got := Str(in); got != in {
			t.Errorf("a %d byte value changed: len %d", MaxBytes, len(got))
		}
	})
	t.Run("over the cap is cut with a count", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("a", MaxBytes+88)
		want := strings.Repeat("a", MaxBytes) + "...[+88 bytes]"
		if got := Str(in); got != want {
			t.Errorf("got len %d tail %q, want tail ...[+88 bytes]", len(got), got[len(got)-14:])
		}
	})
	t.Run("cut falls on a rune boundary", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("\u4e16", 300) // 900 bytes
		got := Str(in)
		if !utf8.ValidString(got) {
			t.Fatalf("cut split a rune: %q", got[len(got)-20:])
		}
		body := got[:strings.Index(got, "...[+")]
		if len(body) != 510 {
			t.Errorf("body is %d bytes, want 510 (170 whole runes)", len(body))
		}
		if !strings.HasSuffix(got, "...[+390 bytes]") {
			t.Errorf("tail = %q, want ...[+390 bytes]", got[len(got)-15:])
		}
	})
	t.Run("an escape is not split by the cap", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("a", MaxBytes-1) + "\n"
		got := Str(in)
		if !strings.HasSuffix(got, "...[+1 bytes]") || strings.Contains(got, `\`) {
			t.Errorf("escape at the cap was split or kept: tail %q", got[len(got)-14:])
		}
	})
}

func TestErr(t *testing.T) {
	t.Parallel()
	if got := Err(nil); got != "<nil>" {
		t.Errorf("Err(nil) = %q, want <nil>", got)
	}
	if got := Err(errors.New("bad\nvalue")); got != `bad\nvalue` {
		t.Errorf("Err = %q, want bad\\nvalue", got)
	}
}

type stringer string

func (s stringer) String() string { return string(s) }

func TestFields_EscapesKeysAndEveryValueKind(t *testing.T) {
	t.Parallel()
	var f Fields
	f.Add("path", "/a\n/b")
	f.Add("err", errors.New("x\ry"))
	f.Add("who", stringer("p\x1bq"))
	f.Add("n", 42)
	f.Add("u", uint8(7))
	f.Add("ok", true)
	f.Add("ratio", 1.5)
	f.Add("none", nil)
	f.Add("k\ney", "v")
	want := []any{
		"path", `/a\n/b`,
		"err", `x\ry`,
		"who", `p\x1bq`,
		"n", "42",
		"u", "7",
		"ok", "true",
		"ratio", "1.5",
		"none", "<nil>",
		`k\ney`, "v",
	}
	got := f.Args()
	if len(got) != len(want) {
		t.Fatalf("Args has %d items, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Args[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestFields_ArgsIsACopy(t *testing.T) {
	t.Parallel()
	var f Fields
	f.Add("a", "b")
	f.Args()[1] = "tampered"
	if got := f.Args()[1]; got != "b" {
		t.Errorf("Args shares storage: second read = %v", got)
	}
}
