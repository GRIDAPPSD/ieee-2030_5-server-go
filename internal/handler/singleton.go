package handler

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/encoding"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// SingletonKey is the fixed key used for singleton sub-resources.
const SingletonKey = "default"

// HandleSingletonGetPut creates a handler for GET/PUT on a singleton
// resource scoped under a parent. Used for DERCapability, DERSettings,
// DERStatus, DERAvailability, DefaultDERControl.
//
// GET returns the stored resource or a default (empty) resource if none exists.
// PUT upserts the resource (creates if not exists, updates if exists).
func HandleSingletonGetPut[T store.Copier[T]](
	scopedStore *memory.ScopedStore[T],
	parentKeyFunc func(r *http.Request) string,
	defaultFactory func(r *http.Request) T,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parentKey := parentKeyFunc(r)

		switch r.Method {
		case http.MethodGet, http.MethodHead:
			resource, err := scopedStore.Get(r.Context(), parentKey, SingletonKey)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					// Return empty default
					def := defaultFactory(r)
					encoding.WriteXML(w, http.StatusOK, &def)
					return
				}
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			encoding.WriteXML(w, http.StatusOK, &resource)

		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read body failed", http.StatusBadRequest)
				return
			}

			var resource T
			if err := xml.Unmarshal(body, &resource); err != nil {
				http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
				return
			}

			// Try create first, update if already exists
			if err := scopedStore.Create(r.Context(), parentKey, SingletonKey, resource); err != nil {
				if errors.Is(err, store.ErrAlreadyExists) {
					st := scopedStore.ForParent(parentKey)
					if err := st.Update(r.Context(), SingletonKey, resource); err != nil {
						http.Error(w, "internal error", http.StatusInternalServerError)
						return
					}
				} else {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
			}

			w.WriteHeader(http.StatusNoContent)

		default:
			encoding.MethodNotAllowed(w, "GET, HEAD, PUT")
		}
	}
}
