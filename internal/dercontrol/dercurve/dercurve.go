// Package dercurve owns how a DERControlBase curve mode names its DERCurve:
// the curve's href, the link to it, and which DERControlBase fields are
// curve links. It depends only on core, so the stores can use it too.
package dercurve

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// ErrBlankID refuses a curve id that is empty or only spaces: its href
// would be "/dc/", the DERCurve list, not a curve.
var ErrBlankID = errors.New("dercurve: curve id is required")

// hrefPrefix is the path GET /dc/{id} serves a curve under; Href and ID
// both read it, so they cannot disagree.
const hrefPrefix = "/dc/"

// Href is the href of the DERCurve stored under id, the one path GET
// /dc/{id} serves it at. A curve's own href and every link to it are
// built here so the two cannot disagree.
func Href(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", ErrBlankID
	}
	return hrefPrefix + id, nil
}

// ID is the id a curve href names, the inverse of Href. ok is false for an
// href Href could not have built.
func ID(href string) (id string, ok bool) {
	id, ok = strings.CutPrefix(href, hrefPrefix)
	if !ok || strings.TrimSpace(id) == "" {
		return "", false
	}
	return id, true
}

// Link links a curve mode to the DERCurve stored under id.
func Link(id string) (*sep2.DERCurveLink, error) {
	href, err := Href(id)
	if err != nil {
		return nil, err
	}
	return &sep2.DERCurveLink{Href: href}, nil
}

var linkType = reflect.TypeFor[*sep2.DERCurveLink]()

// LinkFields names the DERControlBase fields that link a DERCurve, in
// declaration order. It is read from the type, so a curve mode core adds
// is included without an edit here.
func LinkFields() []string {
	return slices.Clone(linkFields())
}

var linkFields = sync.OnceValue(func() []string {
	t := reflect.TypeFor[sep2.DERControlBase]()
	var names []string
	for i := range t.NumField() {
		if t.Field(i).Type == linkType {
			names = append(names, t.Field(i).Name)
		}
	}
	return names
})

// SetLinks links every curve mode spec carries into base. spec is a fixture
// struct whose *string fields are curve ids, each named like the
// DERControlBase field it fills; a nil field is left unset. A *string field
// named like no curve mode is refused, so a misnamed field cannot be
// silently ignored. Errors name the field by its yaml key.
func SetLinks(spec any, base *sep2.DERControlBase) error {
	sv := reflect.Indirect(reflect.ValueOf(spec))
	if sv.Kind() != reflect.Struct {
		return fmt.Errorf("dercurve: spec is %s, not a struct", sv.Kind())
	}
	bv := reflect.ValueOf(base).Elem()
	for i := range sv.NumField() {
		f := sv.Type().Field(i)
		if f.Type != reflect.TypeFor[*string]() {
			continue
		}
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		dst := bv.FieldByName(f.Name)
		if !dst.IsValid() || dst.Type() != linkType {
			return fmt.Errorf("dercurve: %s (%s) is not a DERControlBase curve mode", key, f.Name)
		}
		id, _ := sv.Field(i).Interface().(*string)
		if id == nil {
			continue
		}
		link, err := Link(*id)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		dst.Set(reflect.ValueOf(link))
	}
	return nil
}
