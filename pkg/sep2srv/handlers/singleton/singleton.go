package singleton

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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
	scopedStore store.ScopedStore[T],
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
				srverr.Internal(w, r, err)
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
				srverr.BadRequestMessage(w, r, "invalid XML", err)
				return
			}

			// Try create first, update if already exists
			if err := scopedStore.Create(r.Context(), parentKey, SingletonKey, resource); err != nil {
				if errors.Is(err, store.ErrAlreadyExists) {
					// Update through the scoped contract rather than through a
					// per-parent handle. The parent is known to exist here:
					// Create just reported the resource already present under
					// it, which is the one condition under which the scoped
					// Update's own unknown-parent ErrNotFound cannot fire.
					if err := scopedStore.Update(r.Context(), parentKey, SingletonKey, resource); err != nil {
						srverr.Internal(w, r, err)
						return
					}
				} else {
					srverr.Internal(w, r, err)
					return
				}
			}

			w.WriteHeader(http.StatusNoContent)

		default:
			encoding.MethodNotAllowed(w, "GET, HEAD, PUT")
		}
	}
}
