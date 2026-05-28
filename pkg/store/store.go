package store

import (
	"context"
	"errors"
)

// Copier is a constraint for types that can produce independent copies.
// All sep2 resource types must implement this for immutable store boundaries.
type Copier[T any] interface {
	Copy() T
}

// ResourceStore provides generic CRUD and list operations for IEEE 2030.5 resources.
// Implementations MUST be safe for concurrent use.
// All reads return independent copies — callers may mutate without affecting the store.
type ResourceStore[T Copier[T]] interface {
	Get(ctx context.Context, id string) (T, error)
	List(ctx context.Context, opts ListOptions) (ListResult[T], error)
	Create(ctx context.Context, id string, resource T) error
	Update(ctx context.Context, id string, resource T) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (uint32, error)
}

// ListOptions specifies paging parameters per IEEE 2030.5 section 4.6.2.
type ListOptions struct {
	Start uint32 // s param: first ordinal position (0-based)
	Limit uint32 // l param: max items to return
	After string // a param: return items with keys after this value
}

// ListResult contains a page of results with total count metadata.
type ListResult[T any] struct {
	All     uint32 // total matching resources
	Results uint32 // count returned in this page
	Items   []T
}

// Sentinel errors for store operations.
var (
	ErrNotFound      = errors.New("resource not found")
	ErrAlreadyExists = errors.New("resource already exists")
)
