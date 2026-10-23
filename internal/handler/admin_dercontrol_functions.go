package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	srvmrid "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/mrid"
)

// FunctionsView is a control's functions keyed by DERControlBase element
// name, in the same value shapes a create request takes. It is never nil,
// so the JSON is {} and not null for a control with none.
type FunctionsView map[string]any

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
	Type      *string         `json:"type"`
	Functions json.RawMessage `json:"functions"`
	dercontrol.LegacyValues
}

// controlBaseFromValues is the default-control parse: the values as the
// DERControlBase a program default carries. ok false means the values were
// refused and ref says why. Otherwise a non-nil err is either a
// *dercontrol.RefusalError, values that parsed but did not form a base,
// which the caller words, or a store that could not be read (readFailed).
func controlBaseFromValues(ctx context.Context, curves dercontrol.CurveReader, v controlValuesBody, l *derControlLog) (base *sep2.DERControlBase, ref derControlRefusal, ok bool, err error) {
	var req dercontrol.CreateRequest
	ref, ok, err = buildControlValues(ctx, curves, v, &req, l)
	if err != nil {
		return nil, derControlRefusal{}, true, err
	}
	if !ok {
		return nil, ref, false, nil
	}
	base, err = dercontrol.ControlBase(req)
	return base, derControlRefusal{}, true, err
}

// readFailed reports whether err from controlBaseFromValues is a store that
// could not be read. Any error that is not a refusal counts, so an
// unexpected one answers 500 rather than 400.
func readFailed(err error) bool {
	var refusal *dercontrol.RefusalError
	return err != nil && !errors.As(err, &refusal)
}

// controlValuesReadFailed records a failed store read on l as its cause.
// The error itself is not logged: a store error can carry a path.
func controlValuesReadFailed(l *derControlLog) derControlRefusal {
	l.add("cause", "curve_read_failed")
	return refuseInternal
}

// buildControlValues fills req from a request's type or functions member,
// exactly one of which must be present. A body with neither answers the
// type-required refusal it always has, so a request that names no type
// behaves as before. A curve value is checked against curves; an error is a
// store that could not be read, which refuses nothing.
func buildControlValues(ctx context.Context, curves dercontrol.CurveReader, v controlValuesBody, req *dercontrol.CreateRequest, l *derControlLog) (derControlRefusal, bool, error) {
	hasFunctions := len(v.Functions) > 0 && !bytes.Equal(bytes.TrimSpace(v.Functions), []byte("null"))
	if hasFunctions && v.Type != nil {
		return refuseTypeAndFunctions, false, nil
	}
	if hasFunctions {
		return buildFunctions(ctx, curves, v.Functions, v, req, l)
	}
	if v.Type == nil || *v.Type == "" {
		return refuseTypeRequired, false, nil
	}
	known, attrs, ref := dercontrol.TakeLegacy(*v.Type, v.LegacyValues, req)
	if !known {
		return refuseTypeUnknown, false, nil
	}
	l.add("type", string(req.Type))
	if ref != nil {
		return valueRefusal(ref), false, nil
	}
	for _, a := range attrs {
		l.add(a.Key, a.Value)
	}
	return derControlRefusal{}, true, nil
}

func buildFunctions(ctx context.Context, curves dercontrol.CurveReader, raw json.RawMessage, body controlValuesBody, req *dercontrol.CreateRequest, l *derControlLog) (derControlRefusal, bool, error) {
	if ref := dercontrol.RefuseBesideFunctions(body.LegacyValues); ref != nil {
		return valueRefusal(ref), false, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return refuseFunctionsType, false, nil
	}
	if len(members) == 0 {
		return refuseFunctionsEmpty, false, nil
	}
	for name := range members {
		if !dercontrol.IsFunction(name) {
			return refuseFunctionUnknown, false, nil
		}
	}
	draft, ref, err := dercontrol.DecodeFunctions(ctx, members, curves)
	if err != nil {
		return derControlRefusal{}, false, err
	}
	if ref != nil {
		return valueRefusal(ref), false, nil
	}
	req.Functions = draft
	l.add("functions", strings.Join(slices.Sorted(maps.Keys(members)), ","))
	return derControlRefusal{}, true, nil
}

// valueRefusal answers a refused control value. Its text is the catalog's,
// so it carries no request text.
func valueRefusal(r *dercontrol.ValueRefusal) derControlRefusal {
	return derControlRefusal{http.StatusBadRequest, r.Code, r.Message}
}

// buildCreateRequest validates body field by field so each refusal names
// its field, and records the validated values on l as it goes.
func buildCreateRequest(ctx context.Context, curves dercontrol.CurveReader, body derControlCreateBody, l *derControlLog) (dercontrol.CreateRequest, derControlRefusal, bool) {
	var req dercontrol.CreateRequest
	if body.DERProgramHref == nil || *body.DERProgramHref == "" {
		return req, refuseProgramHrefRequired, false
	}
	if !validProgramHref(*body.DERProgramHref) {
		return req, refuseProgramHrefFormat, false
	}
	req.DERProgramHref = *body.DERProgramHref

	ref, ok, err := buildControlValues(ctx, curves, body.controlValuesBody, &req, l)
	if err != nil {
		return req, controlValuesReadFailed(l), false
	}
	if !ok {
		return req, ref, false
	}

	if body.ExecutesGrant != nil {
		grant, ok := srvmrid.Canonical(*body.ExecutesGrant)
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
