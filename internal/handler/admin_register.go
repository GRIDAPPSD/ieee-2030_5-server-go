package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #159: admin registration assistant.
//
// Three operator-facing endpoints, all under the admin auth surface:
//
//	POST /api/certs/info           parse a PEM cert, derive SFDI/LFDI/etc.
//	GET  /api/devices/by-lfdi/{lfdi}  return EndDevice by LFDI (or found:false)
//	POST /api/devices              create EndDevice + Registration with PIN
//
// SFDI/LFDI derivation reuses core's pkg/sep2tls/identity.go exclusively. No new
// Registration HTTP routes are wired by this file; the resource is persisted
// via RegistrationWriter so the existing /edev/{id}/rg consumer can read it.

// RegistrationWriter is the minimal surface admin_register.go needs to
// persist a Registration resource. The production implementation is
// *memory.Store[sep2.Registration]; tests use a stub. Defined at the
// consumer per the Pike rule.
type RegistrationWriter interface {
	Create(ctx context.Context, id string, resource sep2.Registration) error
}

// certInfoResponse is the shape returned by POST /api/certs/info.
type certInfoResponse struct {
	LFDI               string `json:"lfdi"`
	SFDI               string `json:"sfdi"`
	Subject            string `json:"subject"`
	HardwareModuleName string `json:"hardwareModuleName,omitempty"`
	Fingerprint        string `json:"fingerprint"`
}

// HandleCertInfo parses a PEM-encoded certificate from the request body
// (multipart form field "cert" if multipart, raw body otherwise) and returns
// derived identity material. The certificate is never persisted.
func HandleCertInfo() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pemBytes, err := readCertPEM(r)
		if err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		cert, err := certs.ParseCertificatePEM(pemBytes)
		if err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid certificate")
			return
		}

		fp := sepTLS.Fingerprint(cert)
		resp := certInfoResponse{
			LFDI:        sepTLS.LFDI(cert),
			SFDI:        sepTLS.SFDI(cert),
			Subject:     cert.Subject.String(),
			Fingerprint: hex.EncodeToString(fp[:]),
		}
		if hmn, ok := certs.ExtractHardwareModuleName(cert); ok {
			resp.HardwareModuleName = string(hmn.HWSerialNum)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// readCertPEM extracts the cert PEM from either a multipart "cert" field or
// the raw request body. Limit is 1 MiB - operator-pasted certs are tiny.
func readCertPEM(r *http.Request) ([]byte, error) {
	const limit = 1 << 20

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(limit); err != nil {
			return nil, fmt.Errorf("parse multipart: %w", err)
		}
		if vals := r.MultipartForm.Value["cert"]; len(vals) > 0 {
			return []byte(vals[0]), nil
		}
		f, _, err := r.FormFile("cert")
		if err != nil {
			return nil, fmt.Errorf("multipart cert field missing")
		}
		defer func() { _ = f.Close() }()
		body, err := io.ReadAll(io.LimitReader(f, limit))
		if err != nil {
			return nil, fmt.Errorf("read multipart cert: %w", err)
		}
		return body, nil
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("cert PEM required")
	}
	return body, nil
}

// lookupResponse is the shape returned by GET /api/devices/by-lfdi/{lfdi}.
type lookupResponse struct {
	Found  bool             `json:"found"`
	Device *dashboardDevice `json:"device,omitempty"`
}

type dashboardDevice struct {
	SFDI    string `json:"sfdi"`
	LFDI    string `json:"lfdi"`
	Href    string `json:"href"`
	Enabled bool   `json:"enabled"`
}

// HandleDeviceLookupByLFDI returns a handler for GET /api/devices/by-lfdi/{lfdi}.
// Returns 200 in both cases: {found:false} when no device matches, {found:true,
// device:{...}} otherwise. Missing or malformed LFDI yields 400.
func HandleDeviceLookupByLFDI(s store.EndDeviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lfdi := r.PathValue("lfdi")
		if !validLFDI(lfdi) {
			writeError(w, http.StatusBadRequest, "lfdi must be 40 hex characters")
			return
		}

		dev, err := s.GetByLFDI(r.Context(), strings.ToUpper(lfdi))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSON(w, http.StatusOK, lookupResponse{Found: false})
				return
			}
			writeError(w, http.StatusInternalServerError, "lookup: "+err.Error())
			return
		}
		enabled := dev.Enabled != nil && *dev.Enabled
		writeJSON(w, http.StatusOK, lookupResponse{
			Found: true,
			Device: &dashboardDevice{
				SFDI:    dev.SFDI,
				LFDI:    dev.LFDI,
				Href:    dev.Href,
				Enabled: enabled,
			},
		})
	}
}

// addDeviceRequest is the JSON body for POST /api/devices.
type addDeviceRequest struct {
	SFDI        string `json:"sfdi"`
	LFDI        string `json:"lfdi"`
	Description string `json:"description"`
	PIN         uint32 `json:"pin"`
	Enabled     bool   `json:"enabled"`
}

// addDeviceResponse is the success body for POST /api/devices.
type addDeviceResponse struct {
	Href string `json:"href"`
	SFDI string `json:"sfdi"`
	LFDI string `json:"lfdi"`
}

// HandleAddEndDevice returns a handler for POST /api/devices. Validates
// SFDI/LFDI/PIN, rejects LFDI collisions with 409, persists EndDevice +
// Registration. The registrations writer may be nil; in that case the
// handler returns 503 (we refuse to half-create).
func HandleAddEndDevice(s store.EndDeviceStore, regs RegistrationWriter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if regs == nil {
			writeError(w, http.StatusServiceUnavailable, "registration store not configured")
			return
		}

		var req addDeviceRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		if !sepTLS.ValidateSFDI(req.SFDI) {
			writeError(w, http.StatusBadRequest, "sfdi must be 12 decimal digits with valid check digit")
			return
		}
		if !validLFDI(req.LFDI) {
			writeError(w, http.StatusBadRequest, "lfdi must be 40 hex characters")
			return
		}
		// PIN: uint32 already constrains range. Empty body decodes to 0 which
		// is a valid (if unwise) PIN; surface 0 explicitly so the operator
		// knows what was persisted.

		lfdiUpper := strings.ToUpper(req.LFDI)

		// Collision check by LFDI: 409 if already present.
		if _, err := s.GetByLFDI(r.Context(), lfdiUpper); err == nil {
			writeError(w, http.StatusConflict, "device with this lfdi already registered")
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "lfdi lookup: "+err.Error())
			return
		}

		id := req.SFDI[:8]
		now := time.Now().Unix()
		enabled := req.Enabled
		dev := sep2.EndDevice{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: "/edev/" + id},
			},
			SFDI:                           req.SFDI,
			LFDI:                           lfdiUpper,
			ChangedTime:                    now,
			Enabled:                        &enabled,
			RegistrationLink:               &sep2.Link{Href: fmt.Sprintf("/edev/%s/rg", id)},
			FunctionSetAssignmentsListLink: &sep2.ListLink{Href: fmt.Sprintf("/edev/%s/fsa", id)},
		}

		if err := s.Create(r.Context(), id, dev); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				// SFDI prefix collision with a different LFDI. Surface as 409
				// rather than 500 - operator can pick a different SFDI.
				writeError(w, http.StatusConflict, "device with this sfdi prefix already registered")
				return
			}
			writeError(w, http.StatusInternalServerError, "create end device: "+err.Error())
			return
		}

		reg := sep2.Registration{
			Resource:           sep2.Resource{Href: fmt.Sprintf("/edev/%s/rg", id)},
			DateTimeRegistered: now,
			PIN:                req.PIN,
		}
		if err := regs.Create(r.Context(), id, reg); err != nil {
			// EndDevice exists but Registration write failed. Best-effort
			// rollback so we don't leave a device without a Registration.
			_ = s.Delete(r.Context(), id)
			writeError(w, http.StatusInternalServerError, "create registration: "+err.Error())
			return
		}

		w.Header().Set("Location", dev.Href)
		writeJSON(w, http.StatusCreated, addDeviceResponse{
			Href: dev.Href,
			SFDI: req.SFDI,
			LFDI: lfdiUpper,
		})
	}
}

// validLFDI reports whether s is exactly 40 hex characters (case-insensitive).
// Per IEEE 2030.5 section 6.3.4 the LFDI is the leading 20 bytes of a SHA-256
// fingerprint, so 40 hex chars is the canonical representation.
func validLFDI(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
