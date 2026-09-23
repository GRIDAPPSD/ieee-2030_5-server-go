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
type AdminCertService struct {
	mu        sync.RWMutex
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey
	caCertPEM []byte
}

// NewAdminCertService creates a service with a pre-loaded CA.
func NewAdminCertService(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caCertPEM []byte) *AdminCertService {
	return &AdminCertService{
		caCert:    caCert,
		caKey:     caKey,
		caCertPEM: caCertPEM,
	}
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

// HandleGetCA returns the CA certificate PEM (never the private key). The
// stored file is filtered to its certificate blocks: some tooling writes a
// combined PEM holding the certificate and its private key, and the file
// content must never be echoed verbatim (#644). A stored file that yields no
// certificate block is refused rather than served as an empty 200: a
// scripted caller writes this response straight to a trust anchor file, and
// an empty anchor is a silent failure a human watching a browser would not
// hit the same way.
func (s *AdminCertService) HandleGetCA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.caCertPEM == nil {
			writeError(w, http.StatusServiceUnavailable, "CA not initialized")
			return
		}
		filtered := certs.FilterCertificatePEM(s.caCertPEM)
		if len(filtered) == 0 {
			log.Printf("HandleGetCA: stored CA PEM has no certificate block")
			writeError(w, http.StatusInternalServerError, "CA certificate PEM has no certificate block")
			return
		}
		writeJSON(w, http.StatusOK, certResponse{CertPEM: string(filtered)})
	}
}

// HandleCreateServerCert generates a server certificate signed by the CA.
func (s *AdminCertService) HandleCreateServerCert() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.caCert == nil {
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

		certPEM, keyPEM, err := certs.GenerateServerCert(s.caCert, s.caKey, certs.ServerCertOptions{
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

// HandleCreateDeviceCert generates a device certificate signed by the CA.
// Returns the cert PEM, key PEM, SFDI, and LFDI.
func (s *AdminCertService) HandleCreateDeviceCert() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.caCert == nil {
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

		certPEM, keyPEM, err := certs.GenerateDeviceCert(s.caCert, s.caKey, certs.DeviceCertOptions{
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
