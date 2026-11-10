package content

import (
	"reflect"
	"strings"
)

// shape lists the attributes a type declares and the child elements it can
// hold, read from its xml tags.
type shape struct {
	attrs    map[string]bool
	children map[string]*shape
}

// shapeOf walks t's xml tags. Untagged embedded structs contribute their
// fields to t; seen lets a recursive type point at its own shape.
func shapeOf(t reflect.Type, seen map[reflect.Type]*shape) *shape {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if s, ok := seen[t]; ok {
		return s
	}
	s := &shape{attrs: map[string]bool{}, children: map[string]*shape{}}
	seen[t] = s
	if t.Kind() == reflect.Struct {
		s.add(t, seen)
	}
	return s
}

func (s *shape) add(t reflect.Type, seen map[reflect.Type]*shape) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("xml")
		if !ok && f.Anonymous {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				s.add(ft, seen)
			}
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		if strings.Contains(","+opts+",", ",attr,") {
			s.attrs[name] = true
			continue
		}
		// A "a>b" path names its first element here; deeper levels stay
		// unknown, so an attribute below one is refused.
		first, _, _ := strings.Cut(name, ">")
		s.children[first] = shapeOf(f.Type, seen)
	}
}

// isKnownAttr reports whether node is an attribute the registered type
// declares at that position. An unknown attribute, or one under an element the
// type does not declare, is not known.
func (x *nodeIndex) isKnownAttr(root *shape, node int) bool {
	k := x.keys[node]
	if !strings.HasPrefix(k.name, "@") {
		return false
	}
	var chain []string
	for p := k.parent; p != 0; p = x.keys[p].parent {
		chain = append(chain, x.keys[p].name)
	}
	cur := root
	// chain runs leaf to root and its last entry is the root element, which
	// is the registered type itself.
	for i := len(chain) - 2; i >= 0; i-- {
		next, ok := cur.children[chain[i]]
		if !ok {
			return false
		}
		cur = next
	}
	return cur.attrs[k.name[1:]]
}
