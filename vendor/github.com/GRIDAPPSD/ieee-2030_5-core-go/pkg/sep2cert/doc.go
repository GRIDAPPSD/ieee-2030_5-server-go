// Package sep2cert generates, parses, and validates the ECDSA P-256
// certificates IEEE 2030.5 / CSIP deployments use: root and intermediate
// CAs, server certificates, admin certificates, and device certificates
// carrying an RFC 4108 HardwareModuleName SAN.
//
// A consumer that loads a CA certificate and private key from an
// operator-controlled source (a file, an upload, a minted-in-memory
// pair) should not parse them directly with ParseCertificatePEM and
// ParseKeyPEM: nothing in that path confirms the key actually belongs to
// the certificate, that the certificate may sign, or that it is
// currently valid. Call LoadCA for a file pair or ParseCAPair for PEM
// bytes instead; both apply ValidateCA before returning anything, so a
// pair that cannot legitimately issue certificates is refused with a
// named reason rather than accepted and failing later, silently, at the
// device that tries to verify what it signed.
package sep2cert
