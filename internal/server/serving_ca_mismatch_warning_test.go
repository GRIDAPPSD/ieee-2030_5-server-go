package server

// #638 fix round 1, MEDIUM 2: nothing previously compared the server's own
// leaf against the serving CA the banner advertises as this server's
// anchor. Pointing SEP2_SERVING_CA at a new CA while the leaf is still
// signed by the old one read as healthy; the device-side failure
// ("certificate signed by unknown authority") then has no boot-log signal
// pointing at the cause. servingCAMismatchWarning is a pure function, like
// its adminProxyWarning/metricsExposureWarning siblings, so this test
// drives it directly.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

func TestServingCAMismatchWarning(t *testing.T) {
	dir := t.TempDir()

	servingCACertPEM, servingCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Serving CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(serving): %v", err)
	}
	servingCACert, err := certs.ParseCertificatePEM(servingCACertPEM)
	if err != nil {
		t.Fatalf("parse serving CA: %v", err)
	}
	servingCAKey, err := certs.ParseKeyPEM(servingCAKeyPEM)
	if err != nil {
		t.Fatalf("parse serving CA key: %v", err)
	}

	otherCACertPEM, otherCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Other CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(other): %v", err)
	}
	otherCACert, err := certs.ParseCertificatePEM(otherCACertPEM)
	if err != nil {
		t.Fatalf("parse other CA: %v", err)
	}
	otherCAKey, err := certs.ParseKeyPEM(otherCAKeyPEM)
	if err != nil {
		t.Fatalf("parse other CA key: %v", err)
	}

	matchingLeafPEM, _, err := certs.GenerateServerCert(servingCACert, servingCAKey, certs.ServerCertOptions{
		Hosts: []string{"localhost"}, CommonName: "638 matching leaf", ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert(matching): %v", err)
	}
	mismatchedLeafPEM, _, err := certs.GenerateServerCert(otherCACert, otherCAKey, certs.ServerCertOptions{
		Hosts: []string{"localhost"}, CommonName: "638 mismatched leaf", ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert(mismatched): %v", err)
	}

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	matchingLeafFile := write("matching-leaf.crt", matchingLeafPEM)
	mismatchedLeafFile := write("mismatched-leaf.crt", mismatchedLeafPEM)
	missingLeafFile := filepath.Join(dir, "does-not-exist.crt")
	garbageLeafFile := write("garbage.crt", []byte("not a certificate"))

	t.Run("matching leaf: no warning", func(t *testing.T) {
		got := servingCAMismatchWarning(matchingLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for a leaf actually signed by the serving CA: %q", got)
		}
	})

	t.Run("mismatched leaf: WARNING naming both files and the failure mode", func(t *testing.T) {
		got := servingCAMismatchWarning(mismatchedLeafFile, servingCACert)
		if got == "" {
			t.Fatal("got no warning for a leaf signed by a DIFFERENT CA than the advertised serving CA")
		}
		for _, want := range []string{
			"WARNING",
			mismatchedLeafFile,
			"unknown authority",
			"SEP2_SERVING_CA",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("warning missing fragment %q\n---warning---\n%s", want, got)
			}
		}
	})

	t.Run("nil serving CA: no warning (nothing to compare against)", func(t *testing.T) {
		got := servingCAMismatchWarning(mismatchedLeafFile, nil)
		if got != "" {
			t.Errorf("got warning with a nil serving CA: %q", got)
		}
	})

	t.Run("unreadable leaf file: no warning, not a crash", func(t *testing.T) {
		got := servingCAMismatchWarning(missingLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for an unreadable leaf file: %q", got)
		}
	})

	t.Run("unparseable leaf file: no warning, not a crash", func(t *testing.T) {
		got := servingCAMismatchWarning(garbageLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for an unparseable leaf file: %q", got)
		}
	})
}
