package sep2

// clonePtr returns a pointer to a copy of *p, or nil when p is nil. It is the
// one place a scalar or flat-struct pointer field is cloned; types that hold
// pointers inside the pointee call that type's own Copy instead.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// copier is implemented by the types whose Copy method deep-copies them.
type copier[T any] interface{ Copy() T }

// cloneCopyPtr returns a pointer to p's Copy, or nil when p is nil. Use it for
// a pointer field whose pointee holds pointers of its own.
func cloneCopyPtr[T copier[T]](p *T) *T {
	if p == nil {
		return nil
	}
	v := (*p).Copy()
	return &v
}

// cloneSlice copies a slice of elements that hold no pointers. A nil slice
// stays nil and an empty one stays empty, so the wire shape is unchanged.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	c := make([]T, len(s))
	copy(c, s)
	return c
}

// cloneCopySlice copies a slice by calling each element's Copy, for elements
// that hold pointers. Nil and empty are preserved as in cloneSlice.
func cloneCopySlice[T copier[T]](s []T) []T {
	if s == nil {
		return nil
	}
	c := make([]T, len(s))
	for i, e := range s {
		c[i] = e.Copy()
	}
	return c
}
