package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/content"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// maxContentBody bounds one resource's XML.
const maxContentBody = 1 << 20

// AdminContentHandler is the generic admin endpoint shape for operator-loaded
// content: it maps a request to one content.Owner call and writes nothing
// itself, so the owner stays the only place a body is validated and stored.
//
//	POST   /api/content/{family}/{id}   create from the resource's XML
//	PUT    /api/content/{family}/{id}   replace
//	DELETE /api/content/{family}/{id}   delete
type AdminContentHandler struct {
	Content *content.Owner
}

// ContentBodyTypes are the media types a content write may carry.
var ContentBodyTypes = []string{"application/sep+xml", "application/xml"}

type contentResult struct {
	Family string `json:"family"`
	ID     string `json:"id"`
}

// HandleCreate answers POST /api/content/{family}/{id}.
func (h *AdminContentHandler) HandleCreate() http.HandlerFunc {
	return h.write(http.StatusCreated, h.Content.Create)
}

// HandleReplace answers PUT /api/content/{family}/{id}.
func (h *AdminContentHandler) HandleReplace() http.HandlerFunc {
	return h.write(http.StatusOK, h.Content.Replace)
}

// HandleDelete answers DELETE /api/content/{family}/{id}.
func (h *AdminContentHandler) HandleDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fam, id := r.PathValue("family"), r.PathValue("id")
		if err := h.Content.Delete(r.Context(), fam, id); err != nil {
			writeContentError(w, "delete", err)
			return
		}
		log.Printf("admin content: deleted family=%s id=%s", logsafe.Str(fam), logsafe.Str(id))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *AdminContentHandler) write(status int, op func(ctx context.Context, family, id string, body []byte) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fam, id := r.PathValue("family"), r.PathValue("id")
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxContentBody))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "body unreadable or over 1 MiB")
			return
		}
		if err := op(r.Context(), fam, id, body); err != nil {
			writeContentError(w, "write", err)
			return
		}
		log.Printf("admin content: stored family=%s id=%s", logsafe.Str(fam), logsafe.Str(id))
		writeJSON(w, status, contentResult{Family: fam, ID: id})
	}
}

// writeContentError maps the owner's and the store's sentinels to response
// codes. Only a refusal of the operator's own input carries its text; any
// other error is logged and answered without it.
func writeContentError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, content.ErrUnknownFamily), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such family or resource")
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "resource already exists")
	case errors.Is(err, content.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("admin content: %s: %v", logsafe.Str(op), logsafe.Err(err))
		writeError(w, http.StatusInternalServerError, op+": request failed, see server log")
	}
}
