package auth_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

// goldenFamilies pins what the ACL allowed for each family before the rows
// were generated from the route family table, so a change to the table that
// widens or narrows a family fails here and has to be made on purpose. /mup
// gained PUT and DELETE, which its instance handlers have always served.
var goldenFamilies = map[string]struct {
	methods         string
	unauthenticated bool
}{
	"/dcap": {"GET,HEAD", true},
	"/tm":   {"GET,HEAD", true},
	"/sdev": {"GET,HEAD", false},
	"/edev": {"GET,HEAD,POST,PUT,DELETE", false},
	"/mup":  {"GET,HEAD,POST,PUT,DELETE", false},
	"/dc":   {"GET,HEAD", false},
	"/upt":  {"GET,HEAD,POST", false},
	"/rt":   {"GET,HEAD", false},
	"/msg":  {"GET,HEAD,POST", false},
	"/rsps": {"GET,HEAD,POST", false},
	"/ntfy": {"POST", false},
}

func TestFamilyACLDecisionsMatchGolden(t *testing.T) {
	families := routefamily.All()
	if len(families) != len(goldenFamilies) {
		t.Fatalf("families = %d, golden = %d", len(families), len(goldenFamilies))
	}
	rules := auth.DefaultACLRules()
	if len(rules) != len(families) {
		t.Fatalf("ACL rows = %d, families = %d", len(rules), len(families))
	}

	allMethods := []string{"GET", "HEAD", "PUT", "POST", "DELETE", "PATCH", "OPTIONS"}
	for _, f := range families {
		gold, ok := goldenFamilies[f.Prefix]
		if !ok {
			t.Errorf("family %s has no golden row", f.Prefix)
			continue
		}
		allowed := map[string]bool{}
		for _, m := range strings.Split(gold.methods, ",") {
			allowed[m] = true
		}
		paths := []string{f.Prefix}
		if f.Subtree {
			paths = append(paths, f.Prefix+"/1")
		}
		for _, p := range paths {
			for _, m := range allMethods {
				for _, withCert := range []bool{false, true} {
					want := http.StatusOK
					switch {
					case !allowed[m]:
						want = http.StatusMethodNotAllowed
					case !withCert && !gold.unauthenticated:
						want = http.StatusForbidden
					}
					req := httptest.NewRequest(m, p, nil)
					if withCert {
						req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
					}
					w := httptest.NewRecorder()
					auth.ACLMiddleware(rules)(okHandler()).ServeHTTP(w, req)
					if w.Code != want {
						t.Errorf("%s %s cert=%v: status = %d, want %d", m, p, withCert, w.Code, want)
					}
				}
			}
		}
	}
}
