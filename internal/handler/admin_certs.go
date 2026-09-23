package handler

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
)

// AdminCertService holds the CA state for certificate management.
//
// #622: the serving pair signs this server's own certificates (the server
// leaf minted by HandleCreateServerCert, and what HandleGetCA hands an
// operator); the device pair signs minted device certs
// (HandleCreateDeviceCert). Each handler checks its OWN pair's nil-ness
// rather than a shared one, so a deployment with only one CA loaded still
// serves the routes the other CA covers.
//
// #638 fix round 1: a certificate and its key are now independently
// nil-able within one pair (LoadCAPair loads a certificate whose key is
// missing or does not match it), so the two minting handlers check BOTH
// halves of their own pair, not just the certificate.
type AdminCertService struct {
	mu               sync.RWMutex
	servingCACert    *x509.Certificate
	servingCAKey     *ecdsa.PrivateKey
	servingCACertPEM []byte
	deviceCACert     *x509.Certificate
	deviceCAKey      *ecdsa.PrivateKey
}

// NewAdminCertService creates a service with a single CA signing both the
// serving and device roles. Preserved unchanged for every pre-#622 caller:
// an unsplit deployment builds one CA pair and this wires it to both roles,
// byte for byte with the pre-split behavior.
func NewAdminCertService(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caCertPEM []byte) *AdminCertService {
	return NewAdminCertServiceWithCAs(caCert, caKey, caCertPEM, caCert, caKey)
}

// NewAdminCertServiceWithCAs creates a service with independent serving and
// device CA pairs (#622). Either pair may be nil, which disables only the
// routes that pair signs; see the AdminCertService doc comment. A pair's
// certificate may also be non-nil with its key nil (#638 fix round 1): the
// minting routes for that pair are disabled the same as a fully-nil pair,
// while ServingCA/DeviceCA and HandleGetCA still see the certificate.
func NewAdminCertServiceWithCAs(servingCACert *x509.Certificate, servingCAKey *ecdsa.PrivateKey, servingCACertPEM []byte, deviceCACert *x509.Certificate, deviceCAKey *ecdsa.PrivateKey) *AdminCertService {
	return &AdminCertService{
		servingCACert:    servingCACert,
		servingCAKey:     servingCAKey,
		servingCACertPEM: servingCACertPEM,
		deviceCACert:     deviceCACert,
		deviceCAKey:      deviceCAKey,
	}
}

// ServingCA returns the loaded serving CA certificate, or nil when it is not
// loaded. Read-only accessor for the startup banner (#622); never exposes
// the private key.
func (s *AdminCertService) ServingCA() *x509.Certificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.servingCACert
}

// DeviceCA returns the loaded device CA certificate, or nil when it is not
// loaded. Read-only accessor for the startup banner (#622); never exposes
// the private key.
func (s *AdminCertService) DeviceCA() *x509.Certificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deviceCACert
}

type createDeviceCertRequest struct {
	DeviceType  int    `json:"deviceType"`
	HWSerialNum string `json:"hwSerialNum"`
	HWType      string `json:"hwType"` // manufacturer PEN OID, e.g. "1.3.6.1.4.1.40732.99"
	IsTestCert  bool   `json:"isTestCert"`
}

type createDeviceCertResponse struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
	SFDI    string `json:"sfdi"`
	LFDI    string `json:"lfdi"`
}

type createServerCertRequest struct {
	Hosts      []string `json:"hosts"`
	CommonName string   `json:"commonName"`
	ValidYears int      `json:"validYears"`
}

type certResponse struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// HandleGetCA returns the serving CA certificate PEM (never the private
// key). #622: this is the anchor an operator installs on a device to verify
// THIS server, so it is the serving pair, not the device pair.
func (s *AdminCertService) HandleGetCA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.servingCACertPEM == nil {
			writeError(w, http.StatusServiceUnavailable, "CA not initialized")
			return
		}
		writeJSON(w, http.StatusOK, certResponse{CertPEM: string(s.servingCACertPEM)})
	}
}

// HandleCreateServerCert generates a server certificate signed by the
// serving CA (#622): the protocol listener's own leaf must chain to the CA
// devices are told to trust the server against.
//
// #638 fix round 1 (MEDIUM 3): guards the key alongside the certificate.
// LoadCAPair (#638) can hand this service a certificate with a nil key -
// the deliberate "trusted for verification, not for minting" case - and the
// exported constructor accepts a nil key directly too; certs.GenerateServerCert
// dereferences the key unconditionally, so a cert-only guard here panics.
func (s *AdminCertService) HandleCreateServerCert() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.servingCACert == nil || s.servingCAKey == nil {
			writeError(w, http.StatusServiceUnavailable, "CA not initialized")
			return
		}

		var req createServerCertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if len(req.Hosts) == 0 {
			writeError(w, http.StatusBadRequest, "hosts required")
			return
		}
		if req.ValidYears <= 0 {
			req.ValidYears = 1
		}

		certPEM, keyPEM, err := certs.GenerateServerCert(s.servingCACert, s.servingCAKey, certs.ServerCertOptions{
			Hosts:      req.Hosts,
			CommonName: req.CommonName,
			ValidYears: req.ValidYears,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generate server cert: "+err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, certResponse{
			CertPEM: string(certPEM),
			KeyPEM:  string(keyPEM),
		})
	}
}

// HandleCreateDeviceCert generates a device certificate signed by the
// device CA (#622): the protocol listener's ClientCAs pool must trust it.
// Returns the cert PEM, key PEM, SFDI, and LFDI.
//
// #638 fix round 1 (MEDIUM 3): guards the key alongside the certificate,
// for the same reason as HandleCreateServerCert above.
func (s *AdminCertService) HandleCreateDeviceCert() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.deviceCACert == nil || s.deviceCAKey == nil {
			writeError(w, http.StatusServiceUnavailable, "CA not initialized")
			return
		}

		var req createDeviceCertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.DeviceType < 1 || req.DeviceType > 3 {
			req.DeviceType = 1
		}
		if req.HWSerialNum == "" {
			writeError(w, http.StatusBadRequest, "hwSerialNum is required (CSIP section 6.2 HardwareModuleName SAN)")
			return
		}
		if req.HWType == "" {
			writeError(w, http.StatusBadRequest, "hwType (manufacturer PEN OID) is required")
			return
		}
		hwTypeOID, err := certs.ParseOID(req.HWType)
		if err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid hwType")
			return
		}

		certPEM, keyPEM, err := certs.GenerateDeviceCert(s.deviceCACert, s.deviceCAKey, certs.DeviceCertOptions{
			DeviceType:  certs.DeviceType(req.DeviceType),
			HWType:      hwTypeOID,
			HWSerialNum: req.HWSerialNum,
			IsTestCert:  req.IsTestCert,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generate device cert: "+err.Error())
			return
		}

		cert, err := certs.ParseCertificatePEM(certPEM)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "parse generated cert: "+err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, createDeviceCertResponse{
			CertPEM: string(certPEM),
			KeyPEM:  string(keyPEM),
			SFDI:    sepTLS.SFDI(cert),
			LFDI:    sepTLS.LFDI(cert),
		})
	}
}

// deviceTypesResponse is the shape returned by GET /api/certs/device-types.
type deviceTypesResponse struct {
	DeviceTypes []certs.DeviceTypeInfo `json:"deviceTypes"`
}

// HandleCertDeviceTypes returns the IEEE 2030.5 certificate device types this
// server mints (spec section 6.11.7.1), sourced from certs.AllDeviceTypes so a
// client never carries its own copy of the values. Needs no CA state, so
// BuildAdminRouter registers it even when no cert service is configured.
func HandleCertDeviceTypes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, deviceTypesResponse{DeviceTypes: certs.AllDeviceTypes()})
	}
}
