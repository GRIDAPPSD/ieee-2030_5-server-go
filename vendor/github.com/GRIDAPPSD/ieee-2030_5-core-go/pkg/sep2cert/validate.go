package sep2cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors ValidateCA and ParseCAPair return, so a caller can
// branch with errors.Is instead of matching error text. ValidateCA wraps
// each with the certificate's subject and serial number; it never
// includes key material.
var (
	ErrCAKeyMismatch       = errors.New("CA private key does not match CA certificate public key")
	ErrCAKeyType           = errors.New("CA certificate public key is not ECDSA")
	ErrCANotCA             = errors.New("certificate is not a CA (IsCA or BasicConstraintsValid is false)")
	ErrCAKeyUsage          = errors.New("CA certificate lacks the certificate-signing key usage")
	ErrCAExtKeyUsage       = errors.New("CA certificate's extended key usage excludes client authentication")
	ErrCACriticalExtension = errors.New("CA certificate carries a critical extension this package does not recognize")
	ErrCANotYetValid       = errors.New("CA certificate is not yet valid")
	ErrCAExpired           = errors.New("CA certificate has expired")
	ErrCAExpiringSoon      = errors.New("CA certificate expires within the required minimum remaining validity")
	ErrCACurve             = errors.New("CA private key is not on the P-256 curve")
	ErrCANotSelfSigned     = errors.New("CA certificate is not self-signed")
)

// ValidateCAOptions tunes the checks ValidateCA performs. The zero value
// is the strict default for signing capability, with two exceptions that
// are policy choices rather than facts about the pair: it accepts an
// intermediate (non-self-signed) CA, and it applies no minimum-remaining-
// validity margin.
type ValidateCAOptions struct {
	// Now is the instant ValidateCA checks the validity window against.
	// The zero value means time.Now().
	Now time.Time

	// RequireSelfSigned refuses a CA whose certificate does not verify
	// under its own public key. Off by default: a CA issued by another
	// CA can still legitimately sign device and server certificates,
	// and refusing an intermediate is a deployment policy, not a fact
	// about whether the pair can sign.
	RequireSelfSigned bool

	// MinRemaining, when non-zero, refuses a CA whose certificate
	// expires within this duration of Now, even though it is still
	// technically inside its validity window. Zero skips the check.
	MinRemaining time.Duration
}

// ValidateCA reports why cert and key cannot serve as an issuing CA, or
// nil when they can. It checks, in order: that key is cert's private key
// and both are ECDSA on the P-256 curve the TLS configuration pins; that
// cert is a CA; that cert's key usage permits certificate signing; that
// cert's extended key usage, when present, does not exclude client
// authentication, which would make the CA refuse every device at the
// verify walk; that cert is inside its validity window (and, when
// opts.MinRemaining is set, not about to leave it); and, only when
// opts.RequireSelfSigned, that cert is self-signed.
//
// The returned error names which check failed and identifies the
// certificate by subject and serial number. It never includes key
// material.
func ValidateCA(cert *x509.Certificate, key *ecdsa.PrivateKey, opts ValidateCAOptions) error {
	if cert == nil {
		return fmt.Errorf("validate CA: certificate is nil")
	}
	if key == nil {
		return fmt.Errorf("validate CA: private key is nil")
	}
	if err := validateCA(cert, key, opts); err != nil {
		return fmt.Errorf("CA certificate %q (serial %s): %w", cert.Subject.CommonName, cert.SerialNumber, err)
	}
	return nil
}

func validateCA(cert *x509.Certificate, key *ecdsa.PrivateKey, opts ValidateCAOptions) error {
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return ErrCAKeyType
	}
	if !key.PublicKey.Equal(pub) {
		return ErrCAKeyMismatch
	}
	if key.Curve != elliptic.P256() {
		return ErrCACurve
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return ErrCANotCA
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return ErrCAKeyUsage
	}
	// Verify already refuses a chain carrying an unhandled critical
	// extension (crypto/x509/verify.go); catch it here instead of
	// deferring the refusal to a leaf's verify walk.
	if len(cert.UnhandledCriticalExtensions) > 0 {
		return fmt.Errorf("%w: %v", ErrCACriticalExtension, cert.UnhandledCriticalExtensions)
	}
	// UnknownExtKeyUsage holds OIDs x509 does not recognize; without it here,
	// a CA whose EKU lists only a vendor OID parses with ExtKeyUsage empty,
	// skips this block, and is accepted, deferring the refusal to a device's
	// verify walk instead of catching it at load time.
	if len(cert.ExtKeyUsage) > 0 || len(cert.UnknownExtKeyUsage) > 0 {
		permitted := false
		for _, eku := range cert.ExtKeyUsage {
			if eku == x509.ExtKeyUsageClientAuth || eku == x509.ExtKeyUsageAny {
				permitted = true
				break
			}
		}
		if !permitted {
			return ErrCAExtKeyUsage
		}
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if now.Before(cert.NotBefore) {
		return ErrCANotYetValid
	}
	if now.After(cert.NotAfter) {
		return ErrCAExpired
	}
	if opts.MinRemaining > 0 && cert.NotAfter.Sub(now) < opts.MinRemaining {
		return ErrCAExpiringSoon
	}

	if opts.RequireSelfSigned {
		if err := cert.CheckSignatureFrom(cert); err != nil {
			return fmt.Errorf("%w: %v", ErrCANotSelfSigned, err)
		}
	}

	return nil
}

// ParseCAPair parses a CA certificate and private key and validates the
// pair with ValidateCA before returning either. This is the form an
// upload handler and a caller holding PEM bytes rather than files both
// need: the bridge's embedded server never touches the filesystem for a
// minted dev CA, and an upload handler never gets one either.
func ParseCAPair(certPEM, keyPEM []byte, opts ValidateCAOptions) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}
	key, err := ParseKeyPEM(keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	if err := ValidateCA(cert, key, opts); err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}
