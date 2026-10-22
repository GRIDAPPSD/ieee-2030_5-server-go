// Package logsafe is the one owner of escaping a value before it is written
// to a log line. A value that came from a request, a store or an error text
// can hold a line break, so a text log would take it for the start of a
// second, forged line; the server binary logs JSON and is safe, but code that
// embeds these packages gets the standard library's text output.
//
// Control characters are escaped, never deleted, so an attempt stays visible
// in the log.
package logsafe

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxBytes caps the escaped body of one value; the cut is on an escape
// boundary, and a "...[+N bytes]" tail says how much input was left out.
const MaxBytes = 512

// Str returns s with every control, line-break, bidi and invalid-UTF-8 byte
// written as an escape, the backslash doubled so a typed `\n` cannot pass for
// an escaped one, and the result capped at MaxBytes.
func Str(s string) string {
	var b strings.Builder
	rest := len(s)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		var piece string
		switch {
		case r == utf8.RuneError && size == 1:
			piece = `\x` + hex2(s[i])
		case r == '\\':
			piece = `\\`
		case r == '\n':
			piece = `\n`
		case r == '\r':
			piece = `\r`
		case r == '\t':
			piece = `\t`
		case r < 0x20 || r == 0x7f:
			piece = `\x` + hex2(byte(r))
		case needsUnicodeEscape(r):
			piece = unicodeEscape(r)
		default:
			piece = s[i : i+size]
		}
		if b.Len()+len(piece) > MaxBytes {
			break
		}
		b.WriteString(piece)
		i += size
		rest = len(s) - i
	}
	if rest > 0 {
		b.WriteString("...[+" + strconv.Itoa(rest) + " bytes]")
	}
	// The escapes above already leave no CR or LF; these two calls are the
	// last steps because CodeQL's go/log-injection query recognises
	// strings.ReplaceAll on "\n" and "\r" as the sanitizer.
	out := strings.ReplaceAll(b.String(), "\n", `\n`)
	return strings.ReplaceAll(out, "\r", `\r`)
}

// Err returns Str of the error as fmt prints it, so a nil error and a typed
// nil whose Error method panics both read "<nil>" instead of panicking.
func Err(err error) string {
	return Str(fmt.Sprint(err))
}

// needsUnicodeEscape reports C1 controls, the line and paragraph separators
// and every format character (unicode.Cf: bidi controls, zero-width
// characters, the byte order mark, the tag block), which can reorder or hide
// log text.
func needsUnicodeEscape(r rune) bool {
	return unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Cf, r)
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0xf]})
}

func unicodeEscape(r rune) string {
	if r > 0xffff {
		return fmt.Sprintf(`\U%08x`, r)
	}
	return fmt.Sprintf(`\u%04x`, r)
}

// TruncateOctets cuts s to at most n octets at a character boundary, so the
// result is still valid UTF-8 when s was. It does not escape.
func TruncateOctets(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
