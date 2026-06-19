package handler

import (
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
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
				log.Printf("singleton: GET parent=%q: %v (path=%s)", parentKey, err, r.URL.Path)
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
						log.Printf("singleton: update parent=%q: %v (path=%s)", parentKey, err, r.URL.Path)
						http.Error(w, "internal error", http.StatusInternalServerError)
						return
					}
				} else {
					log.Printf("singleton: create parent=%q: %v (path=%s)", parentKey, err, r.URL.Path)
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
