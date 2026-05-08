package handler

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/identity"
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

// HandleGetCA returns the CA certificate PEM (never the private key).
func (s *AdminCertService) HandleGetCA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if s.caCertPEM == nil {
			writeError(w, http.StatusServiceUnavailable, "CA not initialized")
			return
		}
		writeJSON(w, http.StatusOK, certResponse{CertPEM: string(s.caCertPEM)})
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
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.DeviceType < 1 || req.DeviceType > 3 {
			req.DeviceType = 1
		}

		certPEM, keyPEM, err := certs.GenerateDeviceCert(s.caCert, s.caKey, certs.DeviceCertOptions{
			DeviceType:  certs.DeviceType(req.DeviceType),
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
			SFDI:    identity.SFDI(cert),
			LFDI:    identity.LFDI(cert),
		})
	}
}
