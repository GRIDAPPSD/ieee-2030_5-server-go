package auth

import (
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

// Method bitmaps per spec section 6.2.3.
const (
	MethodGet    uint8 = 0x01
	MethodPut    uint8 = 0x02
	MethodPost   uint8 = 0x04
	MethodDelete uint8 = 0x08
	MethodHead   uint8 = 0x10
)

// AuthType bitmaps per spec section 6.2.3.
const (
	AuthNone       uint8 = 0x01
	AuthUser       uint8 = 0x02
	AuthSelfSigned uint8 = 0x04
	AuthDeviceCert uint8 = 0x08
)

// ACLRule defines access control for a resource path pattern.
type ACLRule struct {
	PathPrefix     string // path prefix to match (e.g., "/edev")
	AllowedMethods uint8  // bitmask of allowed HTTP methods
	RequiredAuth   uint8  // bitmask of allowed auth types
}

// DefaultACLRules returns the default ACL rules for IEEE 2030.5 resources,
// one row per route family.
func DefaultACLRules() []ACLRule {
	families := routefamily.All()
	rules := make([]ACLRule, 0, len(families))
	for _, f := range families {
		var methods uint8
		for _, m := range f.Methods {
			methods |= HTTPMethodToBitmap(m)
		}
		required := AuthDeviceCert
		if f.Unauthenticated {
			required |= AuthNone
		}
		rules = append(rules, ACLRule{PathPrefix: f.Prefix, AllowedMethods: methods, RequiredAuth: required})
	}
	return rules
}

// HTTPMethodToBitmap converts an HTTP method string to the ACL bitmap.
func HTTPMethodToBitmap(method string) uint8 {
	switch method {
	case http.MethodGet:
		return MethodGet
	case http.MethodPut:
		return MethodPut
	case http.MethodPost:
		return MethodPost
	case http.MethodDelete:
		return MethodDelete
	case http.MethodHead:
		return MethodHead
	default:
		return 0
	}
}

// ACLMiddleware enforces access control rules on each request.
// It checks the HTTP method against the ACL rule for the matched path.
func ACLMiddleware(rules []ACLRule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rule := matchRule(rules, r.URL.Path)
			if rule == nil {
				// Fail closed: a path no rule names reaches no handler.
				routefamily.Refuse(w, r)
				return
			}

			methodBit := HTTPMethodToBitmap(r.Method)
			if methodBit == 0 || rule.AllowedMethods&methodBit == 0 {
				w.Header().Set("Allow", allowedMethodsString(rule.AllowedMethods))
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Check auth type
			authType := authTypeFromRequest(r)
			if rule.RequiredAuth&authType == 0 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func matchRule(rules []ACLRule, path string) *ACLRule {
	var best *ACLRule
	bestLen := 0
	for i := range rules {
		if underPrefix(path, rules[i].PathPrefix) && len(rules[i].PathPrefix) > bestLen {
			best = &rules[i]
			bestLen = len(rules[i].PathPrefix)
		}
	}
	return best
}

// underPrefix reports whether path is prefix itself or lies beneath it, on a
// path-segment boundary, so "/edevx" is not under "/edev".
func underPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func authTypeFromRequest(r *http.Request) uint8 {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return AuthNone
	}
	// If we have a peer cert that was verified by our CA pool
	// (RequireAndVerifyClientCert), it's a device cert
	return AuthDeviceCert
}

func allowedMethodsString(methods uint8) string {
	var parts []string
	if methods&MethodGet != 0 {
		parts = append(parts, "GET")
	}
	if methods&MethodHead != 0 {
		parts = append(parts, "HEAD")
	}
	if methods&MethodPut != 0 {
		parts = append(parts, "PUT")
	}
	if methods&MethodPost != 0 {
		parts = append(parts, "POST")
	}
	if methods&MethodDelete != 0 {
		parts = append(parts, "DELETE")
	}
	return strings.Join(parts, ", ")
}
