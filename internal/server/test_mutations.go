//go:build csip_test_hooks

// Build-tag-gated test-only mutation HTTP surface for the CSIP V1.2
// conformance harness. These endpoints simulate utility-side topology and
// program edits that the spec models as out-of-band — they exist solely
// to drive BASIC-003 and MAINT-001/MAINT-003..006 tests and are NOT
// compiled into production binaries.
//
// To enable, build with `-tags csip_test_hooks`. Without the tag, the
// companion stub in test_mutations_notest.go registers no routes and the
// underlying handler code is not part of the binary.
//
// Authentication: even with the tag set, every mutation requires a non-
// empty SEP2_TEST_MUTATION_TOKEN env var to be set at process start AND
// for the request to carry a matching X-CSIP-Test-Token header. This is
// a belt-and-suspenders against a misconfigured production-with-tag build
// exposing mutation endpoints unauthenticated.
//
// IEEE-024 / plan-2 Phase 5.

package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
)

// testMutationTokenEnv names the environment variable that must hold the
// shared secret for X-CSIP-Test-Token. Empty env value disables the
// surface even when the build tag is set.
const testMutationTokenEnv = "SEP2_TEST_MUTATION_TOKEN"

// testMutationTokenHeader is the request header carrying the shared
// secret. Case-insensitive per net/http.
const testMutationTokenHeader = "X-CSIP-Test-Token"

// maxMutationBody bounds JSON payload size so a misuse can't pin memory.
const maxMutationBody = 1 << 16 // 64 KiB

// RegisterMutationHandlers wires the test-only mutation HTTP endpoints
// onto top. It is the entry point called from NewRouter. The build-tag
// stub in test_mutations_notest.go provides a no-op companion for
// production builds.
//
// All routes live under /test/mutations/ and bypass the protocol ACL
// chain — mutations are out-of-band by design.
func RegisterMutationHandlers(top *http.ServeMux, stores *Stores) {
	if stores == nil {
		return
	}
	token := os.Getenv(testMutationTokenEnv)
	if token == "" {
		log.Printf("WARNING: csip_test_hooks build tag set but %s is empty; test mutation surface disabled",
			testMutationTokenEnv)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /test/mutations/edev-delete-oob", handleEdevDeleteOOB(stores))
	mux.HandleFunc("POST /test/mutations/derprog-primacy", handleDERProgPrimacy(stores))
	mux.HandleFunc("POST /test/mutations/derctl-add", handleDERControlAdd(stores))

	top.Handle("/test/mutations/", tokenAuthMiddleware(token, mux))
	log.Printf("csip_test_hooks: test mutation surface enabled at /test/mutations/ (token auth)")
}

// tokenAuthMiddleware rejects requests that do not present the configured
// shared secret in X-CSIP-Test-Token. Comparison uses crypto/subtle so
// equal-length token guesses can't be timed.
func tokenAuthMiddleware(want string, next http.Handler) http.Handler {
	wantBytes := []byte(want)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(testMutationTokenHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), wantBytes) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readJSON decodes the request body into v with a strict size limit. It
// returns a 400-shaped error when the body is missing or malformed.
func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMutationBody+1))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return errors.New("empty body")
	}
	if len(body) > maxMutationBody {
		return fmt.Errorf("body exceeds %d bytes", maxMutationBody)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

// edevDeleteOOBRequest is the JSON body for /test/mutations/edev-delete-oob.
type edevDeleteOOBRequest struct {
	EndDeviceID string `json:"end_device_id"`
}

// handleEdevDeleteOOB deletes an EndDevice from the server's authoritative
// store. Unlike the client-driven DELETE /edev/{id}, this is modeled as
// an out-of-band server-side cleanup: no subscription notification is
// emitted. Used by CSIP MAINT-001 (OOB delete path).
func handleEdevDeleteOOB(stores *Stores) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req edevDeleteOOBRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.EndDeviceID == "" {
			http.Error(w, "bad request: end_device_id required", http.StatusBadRequest)
			return
		}
		if err := stores.EndDevices.Delete(r.Context(), req.EndDeviceID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "end device not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// derProgPrimacyRequest is the JSON body for /test/mutations/derprog-primacy.
type derProgPrimacyRequest struct {
	EndDeviceID string `json:"end_device_id"`
	ProgramID   string `json:"program_id"`
	Primacy     *uint8 `json:"primacy"`
}

// handleDERProgPrimacy updates the Primacy field on an existing DERProgram.
// Used by CSIP MAINT-005 (DERProgram primacy swap).
func handleDERProgPrimacy(stores *Stores) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req derProgPrimacyRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.EndDeviceID == "" {
			http.Error(w, "bad request: end_device_id required", http.StatusBadRequest)
			return
		}
		if req.ProgramID == "" {
			http.Error(w, "bad request: program_id required", http.StatusBadRequest)
			return
		}
		if req.Primacy == nil {
			http.Error(w, "bad request: primacy required", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		inner := stores.DERPrograms.ForParent(req.EndDeviceID)
		existing, err := inner.Get(ctx, req.ProgramID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "der program not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		existing.Primacy = *req.Primacy
		if err := inner.Update(ctx, req.ProgramID, existing); err != nil {
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// derControlAddRequest is the JSON body for /test/mutations/derctl-add.
// The scope (EndDevice / FSA / DERProgram) identifies the parent
// DERProgram under which the new control is appended; control_id is the
// new resource's primary key; control carries the wire payload.
type derControlAddRequest struct {
	EndDeviceID  string          `json:"end_device_id"`
	FSAID        string          `json:"fsa_id"`
	DERProgramID string          `json:"der_program_id"`
	ControlID    string          `json:"control_id"`
	Control      sep2.DERControl `json:"control"`
}

// handleDERControlAdd appends a new DERControl under an existing
// DERProgram. The parent DERProgram must already exist. Used by CSIP
// MAINT-004 (DERControl add to live program).
func handleDERControlAdd(stores *Stores) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req derControlAddRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.EndDeviceID == "" || req.FSAID == "" || req.DERProgramID == "" {
			http.Error(w, "bad request: end_device_id, fsa_id, der_program_id all required", http.StatusBadRequest)
			return
		}
		if req.ControlID == "" {
			http.Error(w, "bad request: control_id required", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		// Verify the parent DERProgram exists. DERPrograms are scoped by
		// EndDeviceID alone (see router.go: scopedListHandler for derp).
		if _, err := stores.DERPrograms.Get(ctx, req.EndDeviceID, req.DERProgramID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "parent der program not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// DERControls are stored under the composite key edev/fsa/derp
		// (see internal/bootfixture/bootfixture.go and router.go's
		// scopedListHandlerDeep). Match that contract.
		key := derControlScope(req.EndDeviceID, req.FSAID, req.DERProgramID)
		if err := stores.DERControls.Create(ctx, key, req.ControlID, req.Control); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				http.Error(w, "control already exists", http.StatusConflict)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// derControlScope mirrors internal/bootfixture.compositeKey. Duplicated
// (one line) rather than exported across an internal/ package boundary;
// flag for dedup if a third caller appears.
func derControlScope(edev, fsa, derp string) string {
	return edev + "/" + fsa + "/" + derp
}
