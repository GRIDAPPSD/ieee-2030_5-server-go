package registration

import (
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

		existing, err := regs.Get(r.Context(), id)
		switch {
		case err == nil:
			existing.PIN = in.PIN
			if err := regs.Update(r.Context(), id, existing); err != nil {
				srverr.Internal(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrNotFound):
			href := memory.RegistrationHref(id)
			reg := sep2.Registration{
				Resource:           sep2.Resource{Href: href},
				DateTimeRegistered: time.Now().Unix(),
				PIN:                in.PIN,
				PollRate:           memory.DefaultRegistrationPollRate,
			}
			if err := regs.Create(r.Context(), id, reg); err != nil {
				srverr.Internal(w, r, err)
				return
			}
			w.Header().Set("Location", href)
			w.WriteHeader(http.StatusCreated)
		default:
			srverr.Internal(w, r, err)
		}
	}
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
