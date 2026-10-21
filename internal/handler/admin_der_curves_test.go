package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func getCurves(t *testing.T, curves handler.CurveLister) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler.HandleListCurves(curves)(w, httptest.NewRequest(http.MethodGet, "/api/der/curves", nil))
	return w
}

func TestListCurves(t *testing.T) {
	s := memory.NewStore[sep2.DERCurve]()
	for _, c := range []struct {
		id   string
		typ  uint8
		desc string
	}{{"vv1", sep2.CurveTypeOpModVoltVar, "volt-var A"}, {"fw", sep2.CurveTypeOpModFreqWatt, ""}} {
		curve := sep2.DERCurve{CurveType: c.typ, Description: c.desc}
		curve.Href = "/dc/" + c.id
		if err := s.Create(t.Context(), c.id, curve); err != nil {
			t.Fatal(err)
		}
	}
	w := getCurves(t, s)
	want := `{"curves":[{"id":"fw","curveType":0,"description":""},{"id":"vv1","curveType":11,"description":"volt-var A"}]}` + "\n"
	if w.Code != http.StatusOK || w.Body.String() != want {
		t.Errorf("status %d body %s, want 200 %s", w.Code, w.Body.String(), want)
	}
}

func TestListCurves_NoneIsAnEmptyList(t *testing.T) {
	w := getCurves(t, memory.NewStore[sep2.DERCurve]())
	if want := `{"curves":[]}` + "\n"; w.Code != http.StatusOK || w.Body.String() != want {
		t.Errorf("status %d body %q, want 200 %q", w.Code, w.Body.String(), want)
	}
}

// A curve a control could not name fails the list rather than going missing
// from it.
func TestListCurves_HrefWithoutAnIDFails(t *testing.T) {
	s := memory.NewStore[sep2.DERCurve]()
	curve := sep2.DERCurve{CurveType: 11}
	curve.Href = "/elsewhere/vv1"
	if err := s.Create(t.Context(), "vv1", curve); err != nil {
		t.Fatal(err)
	}
	if w := getCurves(t, s); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d body %s, want 500", w.Code, w.Body.String())
	}
}

type failingCurves struct{}

func (failingCurves) List(context.Context, store.ListOptions) (store.ListResult[sep2.DERCurve], error) {
	return store.ListResult[sep2.DERCurve]{}, errors.New("disk")
}

func TestListCurves_StoreErrorIs500(t *testing.T) {
	if w := getCurves(t, failingCurves{}); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d body %s, want 500", w.Code, w.Body.String())
	}
}
