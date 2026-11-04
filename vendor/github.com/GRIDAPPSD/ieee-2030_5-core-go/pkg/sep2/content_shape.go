package sep2

import (
	"fmt"
	"unicode/utf8"
)

// maxQuotedPeerText bounds peer-controlled text echoed into an error.
const maxQuotedPeerText = 32

// errUnexpectedText is the one refusal for character data where an element
// must hold child elements. want, when non-empty, names what was expected.
func errUnexpectedText(typeName string, text []byte, want string) error {
	if want != "" {
		return fmt.Errorf("sep2: %s: unexpected text %s, want %s", typeName, quotePeerText(text), want)
	}
	return fmt.Errorf("sep2: %s: unexpected text %s", typeName, quotePeerText(text))
}

// errUnexpectedChild is the one refusal for a child element inside an
// element that must hold text only.
func errUnexpectedChild(local string) error {
	return fmt.Errorf("unexpected child element <%s>", local)
}

// quotePeerText quotes text, cut to maxQuotedPeerText bytes on a rune
// boundary with "..." appended when it was longer.
func quotePeerText(text []byte) string {
	if len(text) <= maxQuotedPeerText {
		return fmt.Sprintf("%q", text)
	}
	n := maxQuotedPeerText
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return fmt.Sprintf("%q", text[:n]) + "..."
}
