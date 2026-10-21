package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// FunctionsView is a control's functions keyed by DERControlBase element
// name, in the same value shapes a create request takes. It is never nil,
// so the JSON is {} and not null for a control with none.
type FunctionsView map[string]any

// FunctionPowerFactorView is opModFixedPFInjectW as a request carries it.
type FunctionPowerFactorView struct {
	Displacement uint16 `json:"displacement"`
	Excitation   bool   `json:"excitation"`
}

func newFunctionsView(b *sep2.DERControlBase) FunctionsView {
	v := FunctionsView{}
	if b == nil {
		return v
	}
	if b.OpModConnect != nil {
		v[dercontrol.FuncConnect] = *b.OpModConnect
	}
	if b.OpModEnergize != nil {
		v[dercontrol.FuncEnergize] = *b.OpModEnergize
	}
	if b.OpModMaxLimW != nil {
		v[dercontrol.FuncMaxLimW] = uint16(*b.OpModMaxLimW)
	}
	if pf := b.OpModFixedPFInjectW; pf != nil {
		v[dercontrol.FuncFixedPFInjectW] = FunctionPowerFactorView{Displacement: pf.Displacement, Excitation: pf.Excitation}
	}
	if tw := b.OpModTargetW; tw != nil {
		v[dercontrol.FuncTargetW] = ActivePowerView{Value: tw.Value, Multiplier: tw.Multiplier}
	}
	return v
}

// HandleFunctions returns the handler for GET /api/der/control-functions.
func (h *AdminDERControlHandler) HandleFunctions() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"functions": dercontrol.Catalog()})
	}
}

// controlValuesBody is the control value members every route that takes
// control values declares: a type with its value, or a functions set. A body
// struct embeds it, so the members parse and refuse the same on every route.
type controlValuesBody struct {
	Type        *string                `json:"type"`
	Functions   json.RawMessage        `json:"functions"`
	MaxLimW     *int64                 `json:"maxLimW"`
	PowerFactor *derControlPowerFactor `json:"powerFactor"`
	TargetW     *derControlTargetW     `json:"targetW"`
}

// controlBaseFromValues is the default-control parse: the values as the
// DERControlBase a program default carries. ok false means the values were
// refused and ref says why; ok true with a non-nil err means they parsed but
// did not form a base, and the caller words that refusal.
func controlBaseFromValues(v controlValuesBody, l *derControlLog) (base *sep2.DERControlBase, ref derControlRefusal, ok bool, err error) {
	var req dercontrol.CreateRequest
	if ref, ok := buildControlValues(v, &req, l); !ok {
		return nil, ref, false, nil
	}
	base, err = dercontrol.ControlBase(req)
	return base, derControlRefusal{}, true, err
}

// buildControlValues fills req from a request's type or functions member,
// exactly one of which must be present. A body with neither answers the
// type-required refusal it always has, so a request that names no type
// behaves as before.
func buildControlValues(v controlValuesBody, req *dercontrol.CreateRequest, l *derControlLog) (derControlRefusal, bool) {
	hasFunctions := len(v.Functions) > 0 && !bytes.Equal(bytes.TrimSpace(v.Functions), []byte("null"))
	if hasFunctions && v.Type != nil {
		return refuseTypeAndFunctions, false
	}
	if hasFunctions {
		return buildFunctions(v.Functions, v, req, l)
	}
	if v.Type == nil || *v.Type == "" {
		return refuseTypeRequired, false
	}
	typ, ok := controlTypes[*v.Type]
	if !ok {
		return refuseTypeUnknown, false
	}
	req.Type = typ
	l.add("type", string(typ))
	return buildValue(typ, v, req, l)
}

func buildFunctions(raw json.RawMessage, body controlValuesBody, req *dercontrol.CreateRequest, l *derControlLog) (derControlRefusal, bool) {
	if body.MaxLimW != nil {
		return refuseMaxLimWBesideFns, false
	}
	if body.PowerFactor != nil {
		return refusePFBesideFns, false
	}
	if body.TargetW != nil {
		return refuseTargetWBesideFns, false
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return refuseFunctionsType, false
	}
	if len(members) == 0 {
		return refuseFunctionsEmpty, false
	}
	known := dercontrol.Catalog()
	for name := range members {
		if !slices.ContainsFunc(known, func(f dercontrol.FunctionInfo) bool { return f.Name == name }) {
			return refuseFunctionUnknown, false
		}
	}

	var set dercontrol.FunctionSet
	for _, f := range known {
		value, present := members[f.Name]
		if !present {
			continue
		}
		ref, ok := parseFunction(f.Name, value, &set)
		if !ok {
			return ref, false
		}
	}
	req.Functions = &set
	l.add("functions", strings.Join(slices.Sorted(maps.Keys(members)), ","))
	return derControlRefusal{}, true
}

// parseFunction decodes one function's value into set. A value of the wrong
// JSON type, null, or with an unknown key answers a refusal naming the
// function, which is always a catalog name and so never request text.
func parseFunction(name string, raw json.RawMessage, set *dercontrol.FunctionSet) (derControlRefusal, bool) {
	wrongType := derControlRefusal{http.StatusBadRequest, "function_value_type", "functions." + name + ": wrong value type"}
	switch name {
	case dercontrol.FuncConnect:
		var v *bool
		if !decodeStrict(raw, &v) || v == nil {
			return wrongType, false
		}
		set.Connect = v
	case dercontrol.FuncEnergize:
		var v *bool
		if !decodeStrict(raw, &v) || v == nil {
			return wrongType, false
		}
		set.Energize = v
	case dercontrol.FuncMaxLimW:
		var v *int64
		if !decodeStrict(raw, &v) || v == nil {
			return wrongType, false
		}
		m, ref, ok := maxLimWValue(*v)
		if !ok {
			return ref, false
		}
		set.MaxLimW = &m
	case dercontrol.FuncFixedPFInjectW:
		var v *derControlPowerFactor
		if !decodeStrict(raw, &v) || v == nil {
			return wrongType, false
		}
		pf, ref, ok := powerFactorValue(v)
		if !ok {
			return ref, false
		}
		set.PowerFactor = pf
	case dercontrol.FuncTargetW:
		var v *derControlTargetW
		if !decodeStrict(raw, &v) || v == nil {
			return wrongType, false
		}
		tw, ref, ok := targetWValue(v)
		if !ok {
			return ref, false
		}
		set.TargetW = tw
	}
	return derControlRefusal{}, true
}

// decodeStrict decodes one JSON value into dst, refusing unknown object keys
// and trailing data.
func decodeStrict(raw json.RawMessage, dst any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// buildCreateRequest validates body field by field so each refusal names
// its field, and records the validated values on l as it goes.
func buildCreateRequest(body derControlCreateBody, l *derControlLog) (dercontrol.CreateRequest, derControlRefusal, bool) {
	var req dercontrol.CreateRequest
	if body.DERProgramHref == nil || *body.DERProgramHref == "" {
		return req, refuseProgramHrefRequired, false
	}
	if !validProgramHref(*body.DERProgramHref) {
		return req, refuseProgramHrefFormat, false
	}
	req.DERProgramHref = *body.DERProgramHref

	if ref, ok := buildControlValues(body.controlValuesBody, &req, l); !ok {
		return req, ref, false
	}

	if body.ExecutesGrant != nil {
		grant, ok := normalizeMRID(*body.ExecutesGrant)
		if !ok {
			return req, refuseExecutesGrantFormat, false
		}
		req.ExecutesGrant = grant
	}

	if body.DurationSeconds == nil {
		return req, refuseDurationRequired, false
	}
	if *body.DurationSeconds <= 0 || *body.DurationSeconds > int64(^uint32(0)) {
		return req, refuseDurationRange, false
	}
	req.DurationSeconds = uint32(*body.DurationSeconds)
	l.add("duration", req.DurationSeconds)

	if body.StartTime != nil {
		if *body.StartTime < 0 {
			return req, refuseStartRange, false
		}
		start := *body.StartTime
		req.Start = &start
		l.add("start", start)
	}

	if body.Description != nil {
		d := *body.Description
		if !validString(d, maxDescriptionOctets) {
			return req, refuseDescription, false
		}
		req.Description = d
	}
	return req, derControlRefusal{}, true
}
