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
			piece = `\u` + hex4(r)
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

// Err returns Str of the error's text, or "<nil>" for a nil error.
func Err(err error) string {
	if err == nil {
		return "<nil>"
	}
	return Str(err.Error())
}

// needsUnicodeEscape reports C1 controls, the line and paragraph separators
// and the bidi and format controls that can reorder or hide log text.
func needsUnicodeEscape(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f, r == 0x2028, r == 0x2029:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0xf]})
}

func hex4(r rune) string {
	return fmt.Sprintf("%04x", r)
}

// Fields accumulates slog key and value arguments with every value escaped.
// Args is the only way a spread slice reaches a log call.
type Fields struct {
	args []any
}

// Add appends key and v. Strings and errors are escaped, a fmt.Stringer is
// escaped through its text, numbers and bools are formatted and escaped, and
// anything else is escaped through its default formatting. The key is
// escaped too.
func (f *Fields) Add(key string, v any) {
	f.args = append(f.args, Str(key), valueString(v))
}

// Args returns the accumulated arguments as a copy.
func (f *Fields) Args() []any {
	return append([]any(nil), f.args...)
}

func valueString(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return Str(x)
	case error:
		return Err(x)
	case fmt.Stringer:
		return Str(x.String())
	case bool:
		return Str(strconv.FormatBool(x))
	case int:
		return Str(strconv.FormatInt(int64(x), 10))
	case int8:
		return Str(strconv.FormatInt(int64(x), 10))
	case int16:
		return Str(strconv.FormatInt(int64(x), 10))
	case int32:
		return Str(strconv.FormatInt(int64(x), 10))
	case int64:
		return Str(strconv.FormatInt(x, 10))
	case uint:
		return Str(strconv.FormatUint(uint64(x), 10))
	case uint8:
		return Str(strconv.FormatUint(uint64(x), 10))
	case uint16:
		return Str(strconv.FormatUint(uint64(x), 10))
	case uint32:
		return Str(strconv.FormatUint(uint64(x), 10))
	case uint64:
		return Str(strconv.FormatUint(x, 10))
	case float32:
		return Str(strconv.FormatFloat(float64(x), 'g', -1, 32))
	case float64:
		return Str(strconv.FormatFloat(x, 'g', -1, 64))
	}
	return Str(fmt.Sprint(v))
}
