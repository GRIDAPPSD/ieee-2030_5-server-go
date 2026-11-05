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
	"path"
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

// maxNesting bounds how deep a body's elements may nest. The deepest 2030.5
// resource is a handful of levels; the bound exists so a hostile body cannot
// make the structural scan do unbounded work.
const maxNesting = 32

// maxNameShown bounds an element or attribute name quoted in a refusal.
const maxNameShown = 40

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
	// Href, when set, names the resource address a body carries, and a body
	// whose last path segment is not the {id} it is sent to is refused. The
	// protocol handler addresses the resource by that href, so a body filed
	// under another id would be served at one address and managed at another.
	// The mRID is not bound: it is the resource's own identity, not its key.
	Href func(T) string
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
	if err := checkSnapshot(path, spec); err != nil {
		return nil, fmt.Errorf("content: family %q: %w", spec.Name, err)
	}
	st, err := memory.NewPersistentStore[T](path, "content "+spec.Name)
	if err != nil {
		return nil, fmt.Errorf("content: family %q: %w", spec.Name, err)
	}
	var root T
	o.families[spec.Name] = &typedFamily[T]{spec: spec, store: st, shape: shapeOf(reflect.TypeOf(root), map[reflect.Type]*shape{})}
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
	// shape is the elements and attributes the registered type can carry,
	// built once at Register.
	shape *shape
}

func (f *typedFamily[T]) create(ctx context.Context, id string, body []byte) error {
	v, err := f.decode(id, body)
	if err != nil {
		return err
	}
	return f.store.Create(ctx, id, v)
}

func (f *typedFamily[T]) replace(ctx context.Context, id string, body []byte) error {
	v, err := f.decode(id, body)
	if err != nil {
		return err
	}
	return f.store.Update(ctx, id, v)
}

func (f *typedFamily[T]) remove(ctx context.Context, id string) error {
	return f.store.Delete(ctx, id)
}

// decode accepts a body only when what the protocol handler will serve is what
// the operator sent: it has one root and nests within bounds, decodes as the
// registered type, passes the family's own check and the id binding, survives
// an encode and decode unchanged, and carries no element or attribute the type
// would drop, nor an element repeated that the type would collapse.
func (f *typedFamily[T]) decode(id string, body []byte) (T, error) {
	var zero T
	idx := newNodeIndex()
	sent, err := idx.count(body)
	if err != nil {
		return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
	}
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
	if err := checkHref(f.spec, v, id); err != nil {
		return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
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
	kept, err := idx.count(out)
	if err != nil {
		return zero, fmt.Errorf("%w: %s: %v", ErrInvalid, f.spec.Name, err)
	}
	for node, n := range sent {
		switch have := kept[node]; {
		case have == 0 && idx.isKnownAttr(f.shape, node):
			// A known attribute written at its zero value is omitted by the
			// encoder and decodes back to the same zero, so nothing is lost.
			continue
		case have == 0:
			return zero, fmt.Errorf("%w: %s: %s is not represented by the type and would be dropped", ErrInvalid, f.spec.Name, idx.describe(node))
		case have < n:
			return zero, fmt.Errorf("%w: %s: %s is repeated and only one value would be kept", ErrInvalid, f.spec.Name, idx.describe(node))
		}
	}
	return v, nil
}

// checkHref applies the family's href-to-id binding, for a write and for a
// record loaded from a snapshot alike.
func checkHref[T store.Copier[T]](spec Spec[T], v T, id string) error {
	if spec.Href == nil {
		return nil
	}
	if path.Base(spec.Href(v)) != id {
		return fmt.Errorf("href does not end in the id %q it is sent to", id)
	}
	return nil
}

// nodeKey names one element or attribute by its parent node and its name.
type nodeKey struct {
	parent int
	name   string
}

// nodeIndex interns element paths as a trie, so the memory a body costs is
// linear in its elements however deep they nest. Node 0 is the document; an
// attribute is a node whose name starts with "@".
type nodeIndex struct {
	ids  map[nodeKey]int
	keys []nodeKey
}

func newNodeIndex() *nodeIndex {
	return &nodeIndex{ids: make(map[nodeKey]int), keys: []nodeKey{{}}}
}

func (x *nodeIndex) child(parent int, name string) int {
	k := nodeKey{parent, name}
	if id, ok := x.ids[k]; ok {
		return id
	}
	id := len(x.keys)
	x.ids[k] = id
	x.keys = append(x.keys, k)
	return id
}

// describe names a node by its own name and its parent's, both shortened.
func (x *nodeIndex) describe(node int) string {
	k := x.keys[node]
	kind, name := "element", k.name
	if strings.HasPrefix(name, "@") {
		kind, name = "attribute", name[1:]
	}
	if k.parent == 0 {
		return fmt.Sprintf("%s %s", kind, shorten(name))
	}
	return fmt.Sprintf("%s %s in %s", kind, shorten(name), shorten(x.keys[k.parent].name))
}

func shorten(name string) string {
	if len(name) > maxNameShown {
		return strings.ToValidUTF8(name[:maxNameShown], "") + "..."
	}
	return name
}

// count returns how many times each element and attribute appears in doc,
// refusing a document with more than one root or nesting past maxNesting.
// Namespace declarations are not counted: they carry no value.
func (x *nodeIndex) count(doc []byte) (map[int]int, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	counts := make(map[int]int)
	stack := []int{0}
	roots := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return counts, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 1 {
				if roots++; roots > 1 {
					return nil, fmt.Errorf("more than one root element, second is %s", shorten(t.Name.Local))
				}
			}
			if len(stack) > maxNesting {
				return nil, fmt.Errorf("element %s nests deeper than %d levels", shorten(t.Name.Local), maxNesting)
			}
			node := x.child(stack[len(stack)-1], t.Name.Local)
			counts[node]++
			stack = append(stack, node)
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				counts[x.child(node, "@"+a.Name.Local)]++
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
}
