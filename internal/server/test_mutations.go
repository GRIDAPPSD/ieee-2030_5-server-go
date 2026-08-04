//go:build csip_test_hooks

// Build-tag-gated test-only mutation HTTP surface for the CSIP V1.2
// conformance harness. These endpoints simulate utility-side topology and
// program edits that the spec models as out-of-band: they exist solely
// to drive BASIC-003 and MAINT-001/MAINT-003..006 and CORE-006 tests and
// are NOT compiled into production binaries.
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
// IEEE-024 (BASIC/MAINT mutations) / IEEE-025 (time-advance) /
// IEEE-078 (FSA swap) / plan-2 Phase 5.

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
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresep2time "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/handlers/sep2time"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
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
// onto top. It is the entry point called from BuildProtocolRouter. The build-tag
// stub in test_mutations_notest.go provides a no-op companion for
// production builds.
//
// The notifier is optional. When non-nil, mutation handlers that change
// a subscribable resource fan a Notification out to subscribed receivers
// after the store mutation commits (IEEE-093: derctl-add -> DERProgramList
// notification, matching CSIP V1.2 section 11.4 / UTIL-004 step 3). nil disables
// the fan-out : production builds never compile this code path, and the
// existing unit tests that pass nil keep working unchanged.
//
// All routes live under /test/mutations/ and bypass the protocol ACL
// chain : mutations are out-of-band by design.
func RegisterMutationHandlers(top *http.ServeMux, stores *Stores, notifier handler.ResourceNotifier) {
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
	mux.HandleFunc("POST /test/mutations/derctl-add", handleDERControlAdd(stores, notifier))
	mux.HandleFunc("POST /test/mutations/time-advance", handleTimeAdvance(stores))
	mux.HandleFunc("POST /test/mutations/fsa-swap", handleFSASwap(stores))
	mux.HandleFunc("POST /test/mutations/subscription-cancel", handleSubscriptionCancel(stores))
	mux.HandleFunc("POST /test/mutations/stress-notify", handleStressNotify(notifier))

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
		// IEEESRV-038: address the program by (parent, id) on the store
		// itself. This used to take a per-parent handle via ForParent,
		// which core IEEECORE-085 withdrew: that method was promoted by
		// an embedded field and was never part of the store.ScopedStore
		// contract, so reaching for it bypassed the persistence wrapper.
		programs := stores.DERPrograms
		existing, err := programs.Get(ctx, req.EndDeviceID, req.ProgramID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "der program not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		existing.Primacy = *req.Primacy
		if err := programs.Update(ctx, req.EndDeviceID, req.ProgramID, existing); err != nil {
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
// MAINT-004 (DERControl add to live program) and CSIP V1.2 section 9.4 /
// UTIL-004 (Utility-Aggregator DER retrieval).
//
// On successful Create the handler fires a Notification on the parent
// DERProgramList href with NotificationStatusChanged. The DERProgramList
// is the resource aggregators subscribe to (see UTIL-003 procedure), so
// fanning out at that href reaches every subscribed aggregator. nil
// notifier disables the fan-out : used by the existing unit tests that
// exercise only the store-mutation side of the hook.
func handleDERControlAdd(stores *Stores, notifier handler.ResourceNotifier) http.HandlerFunc {
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
		// EndDeviceID alone (key = EndDeviceID; this is the contract that
		// core's DER-program handlers and bootfixture both follow).
		if _, err := stores.DERPrograms.Get(ctx, req.EndDeviceID, req.DERProgramID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "parent der program not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// DERControls are stored under the composite key (EndDeviceID,
		// FSAID, DERProgramID). This three-level scope is the contract
		// that core's DER-control handlers and bootfixture both follow.
		key := derControlScope(req.EndDeviceID, req.FSAID, req.DERProgramID)
		if err := stores.DERControls.Create(ctx, key, req.ControlID, req.Control); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				http.Error(w, "control already exists", http.StatusConflict)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// IEEE-093: fan out a "Changed" Notification to subscribers of
		// the parent DERProgramList. Aggregators subscribe to this href
		// in UTIL-003; the new DERControl appearing under one of the
		// programs in that list is the change event. The Manager.Notify
		// path is non-blocking (bounded queue, drops on full).
		if notifier != nil {
			notifier.Notify(ctx, derProgramListHref(req.EndDeviceID, req.FSAID), sep2.NotificationStatusChanged)
		}

		w.WriteHeader(http.StatusCreated)
	}
}

// derProgramListHref returns the canonical href for the DERProgramList
// scoped to (edev, fsa). Mirrors router.go's "GET /edev/{id}/fsa/{fsaId}/derp"
// route. Single source of the path shape so a route rename surfaces here
// at compile time (the route is a string literal in router.go; if/when
// that becomes a typed constant this helper consumes it).
func derProgramListHref(edev, fsa string) string {
	return "/edev/" + edev + "/fsa/" + fsa + "/derp"
}

// derControlScope mirrors internal/bootfixture.compositeKey. Duplicated
// (one line) rather than exported across an internal/ package boundary;
// flag for dedup if a third caller appears.
func derControlScope(edev, fsa, derp string) string {
	return edev + "/" + fsa + "/" + derp
}

// --- /test/mutations/time-advance (IEEE-025, CORE-006) ---

// selfDeviceLogScope is the sentinel parent key under which TM_TIME_ADJUSTED
// LogEvents are persisted. The existing LogEventList store is scoped per
// EndDevice (parent = edev id); CSIP V1.2 CORE-006 requires the event on
// SelfDevice. Using a reserved non-edev key keeps the same store and
// avoids collision with any real EndDevice (whose ids are caller-supplied
// SFDIs/LFDIs, not the literal "sdev").
const selfDeviceLogScope = "sdev"

// LogEvent identifiers for the CSIP V1.2 conformance harness. Function set
// is the standard sep2 Time function set (6); logEventID is project-local
// (the spec leaves vendor-defined numbering inside each function set), so
// we pin a small constant the harness can recognize without ambiguity.
// Reference: IEEE 2030.5 section 10.10, CSIP V1.2 section 4 CORE-006 sentinel
// "TM_TIME_ADJUSTED".
const (
	logEventCodeTimeAdjusted uint8  = 1
	logEventIDTimeAdjusted   uint16 = 1
	logEventTimeAdjustedPEN  uint32 = 0
)

// timeAdvanceRequest is the JSON body for /test/mutations/time-advance.
// Seconds is signed so the harness may rewind as well as advance the
// reported wall clock (CSIP V1.2 CORE-006 prescribes a +1h advance; a
// rewind capability falls out of using a signed offset and helps the
// harness reset between cases).
type timeAdvanceRequest struct {
	Seconds *int64 `json:"seconds"`
}

// handleTimeAdvance shifts the test-only clock offset by the request's
// signed `seconds` value and appends a TM_TIME_ADJUSTED LogEvent to the
// SelfDevice LogEventList. Used by CSIP V1.2 CORE-006.
//
// The actual clock mutation is delegated to coresep2time.AdvanceClock, which
// updates an atomic offset added to time.Now() by coresep2time.HandleTime's
// nowFunc seam (see pkg/sep2srv/handlers/sep2time/time_test_hook.go in core).
// The shift is additive (cumulative across calls) by design: the harness drives
// CORE-006 with a single +3600s advance and then teardown via -<offset>.
func handleTimeAdvance(stores *Stores) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req timeAdvanceRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Seconds == nil {
			http.Error(w, "bad request: seconds required", http.StatusBadRequest)
			return
		}

		// Reject the request before any state mutation if the LogEvent
		// store is missing : CORE-006 requires both the clock shift AND
		// the LogEvent emission, and we'd rather fail the entire op than
		// half-apply it.
		if stores.LogEvents == nil {
			http.Error(w, "internal error: log event store not configured", http.StatusInternalServerError)
			return
		}

		// Shift the clock first; the LogEvent records the post-shift wall
		// time. Read the offset once after the shift to stamp both the
		// LogEvent body and the response envelope from the same snapshot.
		coresep2time.AdvanceClock(time.Duration(*req.Seconds) * time.Second)
		offset := coresep2time.ClockOffset()
		now := time.Now().Add(offset)

		id := fmt.Sprintf("%020d", now.UnixNano())
		evt := sep2.LogEvent{
			Resource:        sep2.Resource{Href: "/sdev/log/" + id},
			CreatedDateTime: now.Unix(),
			Details:         fmt.Sprintf("TM_TIME_ADJUSTED: clock advanced by %d seconds", *req.Seconds),
			FunctionSet:     sep2.FunctionSetTime,
			LogEventCode:    logEventCodeTimeAdjusted,
			LogEventID:      logEventIDTimeAdjusted,
			LogEventPEN:     logEventTimeAdjustedPEN,
			ProfileID:       0,
		}
		if err := stores.LogEvents.Create(r.Context(), selfDeviceLogScope, id, evt); err != nil {
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Encode after WriteHeader: status line already on the wire, no
		// way to convert a downstream write failure into an HTTP error.
		// The payload is small and primitive : encoding itself cannot
		// fail, only the underlying writer can.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"log_event_href": evt.Href,
			"current_time":   now.Unix(),
			"offset_seconds": int64(offset / time.Second),
		})
	}
}

// --- /test/mutations/fsa-swap (IEEE-078, BASIC-003 / MAINT-003) ---

// fsaSwapRequest is the JSON body for /test/mutations/fsa-swap.
//
// FromFSA names the FSA id currently associated with the EndDevice; ToFSA
// is the new id to re-key it under. Both must be non-empty and distinct.
type fsaSwapRequest struct {
	EndDeviceID string `json:"end_device_id"`
	FromFSA     string `json:"from_fsa"`
	ToFSA       string `json:"to_fsa"`
}

// handleFSASwap reassigns the EndDevice's FunctionSetAssignment by re-keying
// the FSA record stored at (end_device_id, from_fsa) to (end_device_id,
// to_fsa). The FSA content (mRID, description, list links) is preserved;
// only Href and the DERProgramListLink.Href are re-stamped to reflect the
// new path. Used by CSIP BASIC-003 (feeder swap) and MAINT-003 (EndDevice->
// FSA reassignment).
//
// Reassignment shape chosen: re-key the FSA list entry under the EndDevice
// scope (option (a) in the IEEE-078 ticket). The other materialization,
// re-keying DERControls under the composite edev/fsa/derp scope, is
// intentionally not performed here: DERPrograms are keyed by EndDevice
// alone (not by FSA), and BASIC-003's feeder-swap procedure is satisfied
// by the FSA list rescoping alone. A harness needing fresh controls under
// the new FSA can chain a derctl-add mutation.
func handleFSASwap(stores *Stores) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fsaSwapRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.EndDeviceID == "" || req.FromFSA == "" || req.ToFSA == "" {
			http.Error(w, "bad request: end_device_id, from_fsa, to_fsa all required", http.StatusBadRequest)
			return
		}
		if req.FromFSA == req.ToFSA {
			http.Error(w, "bad request: from_fsa and to_fsa must differ", http.StatusBadRequest)
			return
		}

		// Guard against an unconfigured FSA store; mirrors the
		// time-advance pre-mutation guard for the LogEvents store.
		if stores.FSAs == nil {
			http.Error(w, "internal error: fsa store not configured", http.StatusInternalServerError)
			return
		}

		ctx := r.Context()

		// Verify the parent EndDevice exists. Without this, a swap under
		// an unknown EndDevice would silently create empty per-parent
		// FSA buckets via ForParent and report success : BASIC-003 needs
		// the explicit 404.
		if _, err := stores.EndDevices.Get(ctx, req.EndDeviceID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "end device not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		fsas := stores.FSAs.ForParent(req.EndDeviceID)

		existing, err := fsas.Get(ctx, req.FromFSA)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "source fsa not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Re-stamp Href on the copy. The store returns an independent
		// copy (Copier contract), so this mutation does not leak back
		// into the source record before the Delete below.
		swapped := existing
		swapped.Href = fmt.Sprintf("/edev/%s/fsa/%s", req.EndDeviceID, req.ToFSA)
		if swapped.DERProgramListLink != nil {
			link := *swapped.DERProgramListLink
			link.Href = fmt.Sprintf("/edev/%s/fsa/%s/derp", req.EndDeviceID, req.ToFSA)
			swapped.DERProgramListLink = &link
		}

		// Create-before-Delete so a Create collision (409) leaves the
		// source FSA in place. Delete-before-Create would orphan the
		// EndDevice mid-swap if the target id were already taken.
		if err := fsas.Create(ctx, req.ToFSA, swapped); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				http.Error(w, "target fsa already exists", http.StatusConflict)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := fsas.Delete(ctx, req.FromFSA); err != nil {
			// Best-effort rollback of the new entry. ErrNotFound on the
			// source between Get and Delete would be a race against a
			// concurrent mutation : surface it instead of swallowing.
			_ = fsas.Delete(ctx, req.ToFSA)
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- /test/mutations/subscription-cancel (IEEE-079, MAINT-006) ---

// subscriptionCancelRequest is the JSON body for /test/mutations/subscription-cancel.
type subscriptionCancelRequest struct {
	SubscriptionID string `json:"subscription_id"`
}

// handleSubscriptionCancel removes a subscription from the server's
// authoritative store AND records its ID in a tombstone set so a
// subsequent POST /edev/{id}/sub that resolves to the same ID is refused
// with 409 Conflict. Used by CSIP V1.2 MAINT-006 (server-side
// subscription terminate, refuses retry).
//
// Tombstone shape chosen: option (a) : a small canceled-id set scoped to
// the test surface, lives in internal/handler/subscription_test_hook.go
// (csip_test_hooks gated). The production HandleCreateSubscription
// consults the set via a nil-checked package-var hook; under no-tag
// builds the hook is never registered and the create path's nil-compare
// is the entire cost. No new field on SubscriptionStore : the
// canceled-id set is process-local test state, not subscription state.
//
// Companion knobs (csip_test_hooks only):
//   - X-CSIP-Test-Subscription-ID header on POST /edev/{id}/sub: lets the
//     harness pin a deterministic ID instead of the auto-generated
//     "sub-<unixnano>" : needed so the harness can drive the same ID
//     into the create path and observe the refusal.
//   - coresub.MarkSubscriptionCanceled / IsSubscriptionCanceled /
//     ResetCanceledSubscriptions: package-level helpers backing the set (in core).
//
// HTTP status codes: 204 success; 400 missing/malformed body; 401
// missing/wrong token (handled by tokenAuthMiddleware); 404 unknown
// subscription_id.
func handleSubscriptionCancel(stores *Stores) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req subscriptionCancelRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.SubscriptionID == "" {
			http.Error(w, "bad request: subscription_id required", http.StatusBadRequest)
			return
		}
		if stores.Subscriptions == nil {
			http.Error(w, "internal error: subscription store not configured", http.StatusInternalServerError)
			return
		}

		// Delete first so a 404 on a never-existing ID does not poison the
		// tombstone set. ErrNotFound surfaces as 404; the tombstone is
		// only recorded after a successful delete.
		if err := stores.Subscriptions.Delete(r.Context(), req.SubscriptionID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "subscription not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Tombstone the ID so a subsequent POST /edev/{id}/sub that
		// resolves to it (via X-CSIP-Test-Subscription-ID) is refused.
		// Idempotent : second cancel of the same ID is a 404 above, never
		// reaches here, which keeps the tombstone reflective of the
		// server's authoritative delete history.
		coresub.MarkSubscriptionCanceled(req.SubscriptionID)

		w.WriteHeader(http.StatusNoContent)
	}
}

// --- /test/mutations/stress-notify (IEEESRV-010) ---

// stressNotifyRequest is the JSON body for /test/mutations/stress-notify.
// Href is the subscribable-resource href to fan notifications to; Status
// is the sep2.NotificationStatus* value (default 2 = Changed). The
// endpoint exists solely to drive the subscription worker pool from the
// stress harness without requiring any pre-existing store object (unlike
// derctl-add which requires a DERProgram parent). Production builds never
// compile this code path.
type stressNotifyRequest struct {
	Href   string `json:"href"`
	Status *uint8 `json:"status,omitempty"`
}

// handleStressNotify calls notifier.Notify for the given href and status.
// Returns 204 on success and 400 on a missing or malformed body. When
// the notifier is nil (e.g. a unit test that passes nil), the endpoint
// is a no-op and returns 204 so callers do not need to guard separately.
//
// The queue is bounded (subscriptionQueueSize=256 by default); the endpoint
// is intentionally non-blocking so the caller can fire at a rate higher than
// the worker pool drains and naturally drive the queue_full counter.
func handleStressNotify(notifier handler.ResourceNotifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stressNotifyRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Href == "" {
			http.Error(w, "bad request: href required", http.StatusBadRequest)
			return
		}
		status := sep2.NotificationStatusChanged
		if req.Status != nil {
			status = *req.Status
		}
		if notifier != nil {
			notifier.Notify(r.Context(), req.Href, status)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// wrapMutationHandlers registers the test-only mutation surface on an
// outer http.ServeMux and falls through to h for all other paths.
// It is called from BuildProtocolRouter in assembly_seam.go so that
// tests which construct the server via server.BuildProtocolRouter get
// the mutation routes on the same handler (mirroring the call that
// lived in the deleted in-tree router.go before IEEESRV-002).
// In production builds the non-tagged companion in test_mutations_notest.go
// returns h unchanged; see IEEE-024.
func wrapMutationHandlers(h http.Handler, stores *Stores, notifier handler.ResourceNotifier) http.Handler {
	top := http.NewServeMux()
	RegisterMutationHandlers(top, stores, notifier)
	top.Handle("/", h)
	return top
}
