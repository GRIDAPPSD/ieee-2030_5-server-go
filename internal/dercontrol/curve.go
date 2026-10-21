package dercontrol

import (
	"errors"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// ErrEmptyCurveID refuses a curve reference with no id: "/dc/" is the
// DERCurve list, not a curve.
var ErrEmptyCurveID = errors.New("dercontrol: curve id is required")

// CurveHref is the href of the DERCurve stored under curveID, the one path
// GET /dc/{id} serves it at. A curve's own href and every DERCurveLink to it
// are built here so the two cannot disagree.
func CurveHref(curveID string) string {
	return "/dc/" + curveID
}

// CurveLink links a DERControlBase curve mode to the DERCurve stored under
// curveID.
func CurveLink(curveID string) (*sep2.DERCurveLink, error) {
	if curveID == "" {
		return nil, ErrEmptyCurveID
	}
	return &sep2.DERCurveLink{Href: CurveHref(curveID)}, nil
}
