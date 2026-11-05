// Package content is the one owner of operator-loaded protocol content: the
// resources of function sets the server authors itself (pricing, billing,
// prepayment and the like), loaded through the admin API.
//
// A family registers its core type and gets back the store its protocol
// handler reads. Every admin endpoint for a family goes through [Owner], so
// validation, the store write, the snapshot under the data directory, the
// rollback on a failed write and the refusal of a corrupt snapshot at boot are
// written once. An endpoint that validated or persisted on its own would be a
// second copy of this.
package content

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

var (
	// ErrInvalid wraps every refusal of a body or an id, so a caller answers
	// 400 without reading the message.
	ErrInvalid = errors.New("content: invalid")
	// ErrUnknownFamily is returned for a family nothing registered.
	ErrUnknownFamily = errors.New("content: unknown family")
)

var (
	familyName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	resourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Spec registers one family of T.
type Spec[T store.Copier[T]] struct {
	// Name is the family's URL segment and, prefixed, its snapshot name.
	Name string
	// Validate refuses a decoded body that lacks what the protocol handler
	// needs, such as a required field. Nil accepts any body that decodes.
	Validate func(T) error
}

type family interface {
	create(ctx context.Context, id string, body []byte) error
	replace(ctx context.Context, id string, body []byte) error
	remove(ctx context.Context, id string) error
}

// Owner holds the registered families.
type Owner struct {
	pathFor func(name string) string

	mu       sync.RWMutex
	families map[string]family
}

// NewOwner returns an Owner that persists a family to pathFor(name). A nil
// pathFor, or one returning "", keeps the family in memory only.
func NewOwner(pathFor func(name string) string) *Owner {
	return &Owner{pathFor: pathFor, families: make(map[string]family)}
}

// Register adds a family and returns the store its protocol handler reads. A
// snapshot that exists but does not load is an error, so a caller that returns
// it refuses to boot rather than serving a family with its content missing.
func Register[T store.Copier[T]](o *Owner, spec Spec[T]) (store.ResourceStore[T], error) {
	if !familyName.MatchString(spec.Name) {
		return nil, fmt.Errorf("content: family name %q is not lower-case letters, digits and hyphens", spec.Name)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, dup := o.families[spec.Name]; dup {
		return nil, fmt.Errorf("content: family %q is already registered", spec.Name)
	}
	path := ""
	if o.pathFor != nil {
		path = o.pathFor("content-" + spec.Name)
	}
	// The persistent store owns the snapshot, the rollback on a failed write
	// and the load-time refusal; an empty path is the plain in-memory store.
	st, err := memory.NewPersistentStore[T](path, "content "+spec.Name)
	if err != nil {
		return nil, fmt.Errorf("content: family %q: %w", spec.Name, err)
	}
	o.families[spec.Name] = &typedFamily[T]{spec: spec, store: st}
	return st, nil
}

// Has reports whether name is a registered family.
func (o *Owner) Has(name string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	_, ok := o.families[name]
	return ok
}

// Create validates body and stores it under id. An id already present is
// store.ErrAlreadyExists.
func (o *Owner) Create(ctx context.Context, name, id string, body []byte) error {
	f, err := o.lookup(name, id)
	if err != nil {
		return err
	}
	return f.create(ctx, id, body)
}

// Replace validates body and replaces the resource under id. An absent id is
// store.ErrNotFound.
func (o *Owner) Replace(ctx context.Context, name, id string, body []byte) error {
	f, err := o.lookup(name, id)
	if err != nil {
		return err
	}
	return f.replace(ctx, id, body)
}

// Delete removes the resource under id. An absent id is store.ErrNotFound.
func (o *Owner) Delete(ctx context.Context, name, id string) error {
	f, err := o.lookup(name, id)
	if err != nil {
		return err
	}
	return f.remove(ctx, id)
}

func (o *Owner) lookup(name, id string) (family, error) {
	o.mu.RLock()
	f, ok := o.families[name]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownFamily, name)
	}
	if !resourceID.MatchString(id) {
		return nil, fmt.Errorf("%w: resource id %q is not 1 to 64 letters, digits, dots, underscores and hyphens", ErrInvalid, id)
	}
	return f, nil
}

type typedFamily[T store.Copier[T]] struct {
	spec  Spec[T]
	store store.ResourceStore[T]
}

func (f *typedFamily[T]) create(ctx context.Context, id string, body []byte) error {
	v, err := f.decode(body)
	if err != nil {
		return err
	}
	return f.store.Create(ctx, id, v)
}

func (f *typedFamily[T]) replace(ctx context.Context, id string, body []byte) error {
	v, err := f.decode(body)
	if err != nil {
		return err
	}
	return f.store.Update(ctx, id, v)
}

func (f *typedFamily[T]) remove(ctx context.Context, id string) error {
	return f.store.Delete(ctx, id)
}

// decode accepts a body only when what the protocol handler will serve is what
// the operator sent: it decodes as the registered type, passes the family's
// own check, survives an encode and decode unchanged, and carries no element
// the type would drop.
func (f *typedFamily[T]) decode(body []byte) (T, error) {
	var zero T
	enc := encoding.NewXMLEncoder()
	var v T
	if err := enc.Unmarshal(body, &v); err != nil {
		return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
	}
	if f.spec.Validate != nil {
		if err := f.spec.Validate(v); err != nil {
			return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
		}
	}
	out, err := enc.Marshal(v)
	if err != nil {
		return zero, fmt.Errorf("%w: %s does not encode: %v", ErrInvalid, f.spec.Name, err)
	}
	var back T
	if err := enc.Unmarshal(out, &back); err != nil {
		return zero, fmt.Errorf("%w: %s does not decode after encoding: %v", ErrInvalid, f.spec.Name, err)
	}
	if !reflect.DeepEqual(v, back) {
		return zero, fmt.Errorf("%w: %s changes when encoded and decoded again", ErrInvalid, f.spec.Name)
	}
	sent, err := elementPaths(body)
	if err != nil {
		return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
	}
	kept, err := elementPaths(out)
	if err != nil {
		return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
	}
	for p := range sent {
		if _, ok := kept[p]; !ok {
			return zero, fmt.Errorf("%w: %s: element %s is not represented by the type and would be dropped", ErrInvalid, f.spec.Name, p)
		}
	}
	return v, nil
}

// elementPaths returns the slash-joined local names of every element in doc.
func elementPaths(doc []byte) (map[string]struct{}, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	paths := make(map[string]struct{})
	var stack []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return paths, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			paths[strings.Join(stack, "/")] = struct{}{}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
}
