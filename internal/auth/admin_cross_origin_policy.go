package auth

import (
	"fmt"
	"net/http"
	"strings"
)

// CrossOriginAny is the SEP2_ADMIN_TRUSTED_ORIGINS value that turns the
// admin plane's cross-origin check off.
const CrossOriginAny = "*"

// defaultCrossOrigin is the policy a nil *CrossOriginPolicy applies: no
// trusted origin, check on.
var defaultCrossOrigin = http.NewCrossOriginProtection()

// CrossOriginPolicy is the one cross-origin decision the admin plane makes.
// The middleware and both panel refusals ask it, so a setting cannot move one
// without the others. A nil or zero-value policy is the default: nothing trusted, check on.
type CrossOriginPolicy struct {
	protection *http.CrossOriginProtection
	off        bool
}

// ParseTrustedOrigins builds the policy for a SEP2_ADMIN_TRUSTED_ORIGINS
// value. Empty is the default policy. A comma-separated list trusts each
// origin. The value, trimmed of surrounding whitespace, equal to "*" turns the
// check off; any other value containing "*" is refused, so a typo such as
// "*," cannot widen the setting. An entry a browser Origin header could never
// equal is refused by name rather than left to fail silently.
func ParseTrustedOrigins(raw string) (*CrossOriginPolicy, error) {
	if strings.TrimSpace(raw) == CrossOriginAny {
		return &CrossOriginPolicy{off: true}, nil
	}
	var entries []string
	for _, e := range strings.Split(raw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	p := &CrossOriginPolicy{protection: http.NewCrossOriginProtection()}
	for _, e := range entries {
		if strings.Contains(e, "*") {
			return nil, fmt.Errorf("SEP2_ADMIN_TRUSTED_ORIGINS: entry %q: %q is only valid as the whole value, and a wildcard never matches a browser origin", e, CrossOriginAny)
		}
		if err := p.protection.AddTrustedOrigin(e); err != nil {
			return nil, fmt.Errorf("SEP2_ADMIN_TRUSTED_ORIGINS: origin %q: %w", e, err)
		}
		if reason := unmatchableOrigin(e); reason != "" {
			return nil, fmt.Errorf("SEP2_ADMIN_TRUSTED_ORIGINS: origin %q: %s", e, reason)
		}
	}
	return p, nil
}

// unmatchableOrigin explains why an origin that parses would never equal the
// Origin header a browser sends: the stdlib matches the string exactly, and
// browsers send a lowercase scheme and host with the default port omitted.
func unmatchableOrigin(e string) string {
	scheme, rest, _ := strings.Cut(e, "://")
	switch {
	case scheme != strings.ToLower(scheme):
		return "the scheme must be lowercase"
	case strings.ContainsAny(rest, "/?#"):
		return "an origin has no path, query or fragment"
	case strings.Contains(rest, "@"):
		return "an origin has no userinfo"
	case rest != strings.ToLower(rest):
		return "the host must be lowercase"
	case (scheme == "http" && strings.HasSuffix(rest, ":80")) || (scheme == "https" && strings.HasSuffix(rest, ":443")):
		return "the default port must be omitted"
	}
	return ""
}

// Disabled reports whether the check is off.
func (p *CrossOriginPolicy) Disabled() bool { return p != nil && p.off }

// Check returns the refusal for a request the policy does not admit.
func (p *CrossOriginPolicy) Check(r *http.Request) error {
	switch {
	case p == nil || (!p.off && p.protection == nil):
		return defaultCrossOrigin.Check(r)
	case p.off:
		return nil
	default:
		return p.protection.Check(r)
	}
}
