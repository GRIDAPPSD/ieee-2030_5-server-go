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
// without the others. A nil policy is the default: nothing trusted, check on.
type CrossOriginPolicy struct {
	protection *http.CrossOriginProtection
	off        bool
}

// ParseTrustedOrigins builds the policy for a SEP2_ADMIN_TRUSTED_ORIGINS
// value. Empty is the default policy. A comma-separated list trusts each
// origin, and a malformed one is an error naming it. Exactly "*" turns the
// check off; "*" beside any other entry is refused, so a typo cannot widen
// the setting.
func ParseTrustedOrigins(raw string) (*CrossOriginPolicy, error) {
	var entries []string
	for _, e := range strings.Split(raw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	for _, e := range entries {
		if e == CrossOriginAny {
			if len(entries) > 1 {
				return nil, fmt.Errorf("SEP2_ADMIN_TRUSTED_ORIGINS: %q cannot be combined with other entries", CrossOriginAny)
			}
			return &CrossOriginPolicy{off: true}, nil
		}
	}
	p := &CrossOriginPolicy{protection: http.NewCrossOriginProtection()}
	for _, e := range entries {
		if err := p.protection.AddTrustedOrigin(e); err != nil {
			return nil, fmt.Errorf("SEP2_ADMIN_TRUSTED_ORIGINS: origin %q: %w", e, err)
		}
	}
	return p, nil
}

// Disabled reports whether the check is off.
func (p *CrossOriginPolicy) Disabled() bool { return p != nil && p.off }

// Check returns the refusal for a request the policy does not admit.
func (p *CrossOriginPolicy) Check(r *http.Request) error {
	switch {
	case p == nil:
		return defaultCrossOrigin.Check(r)
	case p.off:
		return nil
	default:
		return p.protection.Check(r)
	}
}
