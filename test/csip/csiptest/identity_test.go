package csiptest_test

import (
	"context"
	"crypto/x509"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

var canonicalLFDI = regexp.MustCompile(`^[0-9A-F]{40}$`)

func TestNewDeviceIdentity_CarriesTheIdentityOfItsOwnCertificate(t *testing.T) {
	t.Parallel()
	id := csiptest.NewDeviceIdentity(t, "IDENTITY-TEST")

	leaf, err := x509.ParseCertificate(id.Cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if got := sepTLS.LFDI(leaf); got != id.LFDI || !canonicalLFDI.MatchString(id.LFDI) {
		t.Errorf("identity LFDI %q, certificate LFDI %q; want equal and 40 upper-case hex", id.LFDI, got)
	}
	if got := sepTLS.SFDI(leaf); got != id.SFDI || id.SFDI == "" {
		t.Errorf("identity SFDI %q, certificate SFDI %q; want equal and non-empty", id.SFDI, got)
	}
	if _, err := os.Stat(id.CAFile); err != nil {
		t.Errorf("CA file: %v", err)
	}
	if other := csiptest.NewDeviceIdentity(t, "IDENTITY-TEST"); other.LFDI == id.LFDI {
		t.Error("two identities share an LFDI; each must carry its own key")
	}
}

func TestBootServer_WithDeviceIdentityPresentsThatCertificate(t *testing.T) {
	t.Parallel()
	id := csiptest.NewDeviceIdentity(t, "BOOT-IDENTITY")
	srv := csiptest.BootServer(t, csiptest.WithDeviceIdentity(id))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.BaseURL+"/edev",
		strings.NewReader(`<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := srv.HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev status %d, want 201; body=%s", resp.StatusCode, body)
	}
	var created sep2.EndDevice
	if err := xml.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if created.LFDI != id.LFDI || created.SFDI != id.SFDI {
		t.Errorf("server registered LFDI %q SFDI %q, want the presented identity %q %q", created.LFDI, created.SFDI, id.LFDI, id.SFDI)
	}
}
