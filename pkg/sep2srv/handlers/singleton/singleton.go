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

// Option adjusts a singleton handler.
type Option[T any] func(*options[T])

type options[T any] struct {
	stamp  func(r *http.Request, resource *T)
	parent func(r *http.Request) error
}

// WithParent makes the singleton exist only under a parent that exists, as
// reported by exists (store.ErrNotFound for absent, anything else a failed
// check). GET and PUT under an absent parent answer 404.
//
// A PUT checks again after its write and removes what it wrote when the
// parent has gone: a cascade that deletes the parent before its children
// (the EndDevice delete removes DERs first) cannot then leave a child that a
// later parent at the same key would serve.
func WithParent[T any](exists func(r *http.Request) error) Option[T] {
	return func(o *options[T]) { o.parent = exists }
}

// WithStamp sets fields of a PUT body from the request path before it is
// stored, so a client cannot choose them (the self href above all).
func WithStamp[T any](stamp func(r *http.Request, resource *T)) Option[T] {
	return func(o *options[T]) { o.stamp = stamp }
}

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
	opts ...Option[T],
) http.HandlerFunc {
	var o options[T]
	for _, opt := range opts {
		opt(&o)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		parentKey := parentKeyFunc(r)

		switch r.Method {
		case http.MethodGet, http.MethodHead:
			if !parentPresent(w, r, o.parent) {
				return
			}
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
			if !parentPresent(w, r, o.parent) {
				return
			}
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

			if o.stamp != nil {
				o.stamp(r, &resource)
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

			if err := parentCheck(r, o.parent); err != nil {
				// The record is removed even when the check itself failed: an
				// unknown parent is treated as gone so no orphan can survive, and
				// the client retries on the 500. The response is decided after
				// the Delete, because a failed Delete must answer 500, not 404.
				if derr := scopedStore.Delete(r.Context(), parentKey, SingletonKey); derr != nil && !errors.Is(derr, store.ErrNotFound) {
					srverr.Internal(w, r, derr)
					return
				}
				answerParentErr(w, r, err)
				return
			}

			w.WriteHeader(http.StatusNoContent)

		default:
			encoding.MethodNotAllowed(w, "GET, HEAD, PUT")
		}
	}
}

// parentCheck reports whether the parent exists without writing a response; a
// nil exists means the route has no parent to require.
func parentCheck(r *http.Request, exists func(r *http.Request) error) error {
	if exists == nil {
		return nil
	}
	return exists(r)
}

// parentPresent answers 404 or 500 and returns false unless exists reports the
// parent.
func parentPresent(w http.ResponseWriter, r *http.Request, exists func(r *http.Request) error) bool {
	err := parentCheck(r, exists)
	if err == nil {
		return true
	}
	answerParentErr(w, r, err)
	return false
}

// answerParentErr writes the 404 for an absent parent and a logged 500 for any
// other failed check.
func answerParentErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	srverr.Internal(w, r, err)
}
