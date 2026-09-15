package dercontrol

import (
	"context"
	"strings"
	"testing"
)

// Acceptance criterion 9: every refusal returns a distinct error the caller
// can map to a fixed message, and no error text includes a request value.
// The "store holds no new record after each refusal" half is asserted
// alongside each refusal test in the other _test.go files via
// assertNoNewControl; this file covers the two properties that cut across
// all of them.

// TestRefusalError_CodesAreDistinctMessages proves RefusalCode values map to
// distinct, stable Error() strings: a caller switching on the string (or,
// better, on Code via errors.As) never conflates two refusal reasons.
func TestRefusalError_CodesAreDistinctMessages(t *testing.T) {
	codes := []RefusalCode{
		RefusalPENNotConfigured,
		RefusalInvalidProgramHref,
		RefusalProgramNotFound,
		RefusalNoControlListLink,
		RefusalStartInPast,
		RefusalStartTooFarAhead,
		RefusalDurationOutOfRange,
		RefusalUnknownType,
		RefusalMissingValue,
		RefusalUnexpectedValue,
		RefusalValueOutOfRange,
		RefusalControlNotFound,
		RefusalAlreadyCancelled,
		RefusalAlreadySuperseded,
		RefusalEnded,
	}
	seen := make(map[string]RefusalCode, len(codes))
	for _, c := range codes {
		msg := (&RefusalError{Code: c}).Error()
		if other, dup := seen[msg]; dup {
			t.Fatalf("codes %q and %q produce the same Error() text %q", other, c, msg)
		}
		seen[msg] = c
	}
}

// TestIssue_RefusalTextNeverContainsRequestValue drives a request carrying
// an attacker-shaped value through every refusal path this test file can
// reach directly, and asserts the injected value never appears in the
// error text: RefusalError.Error() renders only its fixed Code by
// construction, but this proves it end to end from Issue rather than by
// reading the source.
func TestIssue_RefusalTextNeverContainsRequestValue(t *testing.T) {
	const poison = "><script>evil-mrid-forgery-attempt"

	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  "/edev/" + poison + "/fsa/0/derp/p1",
		Type:            Connect,
		DurationSeconds: 3600,
	})
	assertRefusal(t, err, RefusalProgramNotFound)
	if strings.Contains(err.Error(), poison) {
		t.Fatalf("error text %q leaked the request value", err.Error())
	}

	_, err2 := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            ControlType(poison),
		DurationSeconds: 3600,
	})
	assertRefusal(t, err2, RefusalUnknownType)
	if strings.Contains(err2.Error(), poison) {
		t.Fatalf("error text %q leaked the request value", err2.Error())
	}
	assertNoNewControl(t, h)
}
