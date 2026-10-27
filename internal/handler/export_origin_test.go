package handler

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// IssueAcrossForTest is the server-side path that sets a control's origin:
// req.Origin, carried to every copy's lifecycle record and to the FSA's
// record by recorder. The DERMS request route calls issueAcross and recorder
// the same way.
func (h *AdminFSAControlHandler) IssueAcrossForTest(ctx context.Context, req dercontrol.CreateRequest, devices []string, fsaID, tmplID string) (dercontrol.AcrossResult, error) {
	return h.issueAcross(ctx, req, devices, fsaID, tmplID, h.recorder(fsaID, tmplID, req.Origin))
}
