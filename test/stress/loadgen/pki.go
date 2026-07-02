package loadgen

import (
	"crypto/tls"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// PKISet holds a generated CA and N device certs for a stress run.
type PKISet struct {
	CACertPEM []byte
	CAKeyPEM  []byte
	Devices   []DeviceCert
}

// DeviceCert holds the PEM-encoded cert and key for one virtual client.
type DeviceCert struct {
	CertPEM []byte
	KeyPEM  []byte
}

// GeneratePKI generates a root CA and count device certs.
// Each device cert gets a unique HWSerialNum of the form "STRESS-<seed>-<idx>".
// This is CPU-bound: plan for ~5-10ms per cert on a modern core.
func GeneratePKI(seed uint64, count int) (*PKISet, error) {
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: fmt.Sprintf("SEP2StressCA-%d", seed),
		ValidYears: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("generate CA: %w", err)
	}

	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}

	ps := &PKISet{
		CACertPEM: caCertPEM,
		CAKeyPEM:  caKeyPEM,
		Devices:   make([]DeviceCert, count),
	}

	for i := 0; i < count; i++ {
		certPEM, keyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
			DeviceType:  certs.DeviceTypeGeneric,
			HWSerialNum: fmt.Sprintf("STRESS-%d-%05d", seed, i),
		})
		if err != nil {
			return nil, fmt.Errorf("generate device cert %d: %w", i, err)
		}
		ps.Devices[i] = DeviceCert{CertPEM: certPEM, KeyPEM: keyPEM}
	}
	return ps, nil
}

// TLSCert returns a *tls.Certificate for the idx-th device.
func (ps *PKISet) TLSCert(idx int) (tls.Certificate, error) {
	if idx < 0 || idx >= len(ps.Devices) {
		return tls.Certificate{}, fmt.Errorf("pki: idx %d out of range [0,%d)", idx, len(ps.Devices))
	}
	return tls.X509KeyPair(ps.Devices[idx].CertPEM, ps.Devices[idx].KeyPEM)
}

// CertFunc returns a func(idx int) (certPEM, keyPEM []byte, err error) suitable
// for passing to Config.ClientCert. It reads from the pre-generated PKISet.
func (ps *PKISet) CertFunc() func(int) ([]byte, []byte, error) {
	return func(idx int) ([]byte, []byte, error) {
		if idx < 0 || idx >= len(ps.Devices) {
			return nil, nil, fmt.Errorf("pki: idx %d out of range [0,%d)", idx, len(ps.Devices))
		}
		return ps.Devices[idx].CertPEM, ps.Devices[idx].KeyPEM, nil
	}
}
