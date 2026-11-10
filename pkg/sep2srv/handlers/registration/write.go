package registration

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// maxPIN is the largest pIN sep.xsd's PINType allows (six decimal digits).
const maxPIN = 999999

// HandlePutRegistration returns the handler for PUT /edev/{id}/rg.
//
// It takes no authority decision: it is mounted only behind the manager-only
// check, which has already confirmed the caller and that the EndDevice exists.
// Only the pIN comes from the body. The href, dateTimeRegistered and pollRate
// are server-authored, so a replacement keeps the first two and the pollRate
// the record already had.
//
// 201 with Location when the device had no Registration, 204 when one was
// replaced, 400 for a malformed body or a pIN above 999999.
func HandlePutRegistration(regs store.ResourceStore[sep2.Registration]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			encoding.MethodNotAllowed(w, "PUT")
			return
		}
		id := r.PathValue("id")

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			srverr.LogBadRequest(r, err)
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		var in sep2.Registration
		if err := xml.Unmarshal(body, &in); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}
		if in.PIN > maxPIN {
			http.Error(w, "pIN must be at most 999999", http.StatusBadRequest)
			return
		}

		created, err := upsert(r.Context(), regs, id, in.PIN)
		if err != nil {
			srverr.Internal(w, r, err)
			return
		}
		if created {
			w.Header().Set("Location", memory.RegistrationHref(id))
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// upserter is the one-call create-or-replace a store may offer. The memory
// RegistrationStore does, and it is what makes the PUT atomic.
type upserter interface {
	Upsert(ctx context.Context, id string, build func(current sep2.Registration, found bool) sep2.Registration) (created bool, err error)
}

// maxUpsertAttempts bounds the read-then-write fallback for a store without
// Upsert: each retry follows a writer that changed the record between the read
// and the write, so a few attempts is all contention can need.
const maxUpsertAttempts = 4

// upsert stores the pIN for id, keeping the record's href, registration time
// and poll rate when it exists. A store without Upsert gets a read followed by
// an update or a create, retried when a concurrent writer made the read stale.
func upsert(ctx context.Context, regs store.ResourceStore[sep2.Registration], id string, pin uint32) (created bool, err error) {
	build := func(cur sep2.Registration, found bool) sep2.Registration {
		if found {
			cur.PIN = pin
			return cur
		}
		return sep2.Registration{
			Resource:           sep2.Resource{Href: memory.RegistrationHref(id)},
			DateTimeRegistered: time.Now().Unix(),
			PIN:                pin,
			PollRate:           memory.DefaultRegistrationPollRate,
		}
	}
	if u, ok := regs.(upserter); ok {
		return u.Upsert(ctx, id, build)
	}
	for range maxUpsertAttempts {
		cur, gerr := regs.Get(ctx, id)
		switch {
		case gerr == nil:
			err = regs.Update(ctx, id, build(cur, true))
			created = false
		case errors.Is(gerr, store.ErrNotFound):
			err = regs.Create(ctx, id, build(sep2.Registration{}, false))
			created = true
		default:
			return false, gerr
		}
		if err == nil || !(errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrAlreadyExists)) {
			return created, err
		}
	}
	return false, err
}

// HandleDeleteRegistration returns the handler for DELETE /edev/{id}/rg. Like
// the PUT it is mounted only behind the manager-only check. It answers 200
// with the Registration it removed, which is what the WADL declares for
// DELETE, and 404 when the device has none.
func HandleDeleteRegistration(regs store.ResourceStore[sep2.Registration]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			encoding.MethodNotAllowed(w, "DELETE")
			return
		}
		id := r.PathValue("id")

		reg, err := regs.Get(r.Context(), id)
		if err == nil {
			err = regs.Delete(r.Context(), id)
		}
		switch {
		case err == nil:
			encoding.WriteXML(w, http.StatusOK, &reg)
		case errors.Is(err, store.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			srverr.Internal(w, r, err)
		}
	}
}
