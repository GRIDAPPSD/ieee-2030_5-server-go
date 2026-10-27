package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #972: a control on an FSA's program template reaches every device the FSA
// is assigned to, including devices assigned later.
//
//	POST /api/fsas/{id}/der-programs/{derpId}/controls                       create
//	GET  /api/fsas/{id}/der-programs/{derpId}/controls                       list
//	POST /api/fsas/{id}/der-programs/{derpId}/controls/{controlId}/cancel    cancel
//
// The FSA keeps one record per control; each assigned device's copy of the
// template holds a copy of the control with the record's id, mRID and
// creationTime.

// AdminFSAControlHandler serves the FSA control routes. FSAs owns the FSA and
// program stores and the lock every program write takes; Controls supplies
// the fleet checks, the cancel path and the audit log; Issuer writes the
// copies. All three are required.
type AdminFSAControlHandler struct {
	FSAs     *AdminFSAHandler
	Controls *AdminDERControlHandler
	Issuer   FSAControlIssuer
}

// FSAControlIssuer writes one control to every copy of a template, and the
// recorded controls to a copy made later. The production implementation is
// *dercontrol.Issuer.
type FSAControlIssuer interface {
	IssueAcross(ctx context.Context, req dercontrol.CreateRequest, targets []dercontrol.Target, record dercontrol.RecordWriter) (dercontrol.AcrossResult, error)
	Replicate(ctx context.Context, replicas []dercontrol.Replica) ([]dercontrol.Result, error)
}

// FSAControlReplicator writes the live controls of an FSA's templates to a
// device just assigned to it.
type FSAControlReplicator interface {
	ReplicateTo(ctx context.Context, deviceID, fsaID string) error
}

// FSAControlFinder finds the FSA control record carrying an mRID.
type FSAControlFinder interface {
	FSAControlByMRID(mrid string) (fsaID string, c memory.FSAControl, ok bool)
}

// fsaControlBody is the create body: a control body with no derProgramHref,
// since the template names the program, and no executesGrant, since a grant
// is carried out by one device.
type fsaControlBody struct {
	controlValuesBody
	StartTime       *int64  `json:"startTime"`
	DurationSeconds *int64  `json:"durationSeconds"`
	Description     *string `json:"description"`
}

// FSAControlCopyView is one assigned device's copy of an FSA control.
// EventStatus is null when the device holds no copy.
type FSAControlCopyView struct {
	Device      string           `json:"device"`
	Href        string           `json:"href"`
	EventStatus *EventStatusView `json:"eventStatus"`
}

// FSAControlView is one FSA control record with the copies of the devices
// assigned to its FSA. Copies is always an array.
type FSAControlView struct {
	ID             string             `json:"id"`
	MRID           string             `json:"mRID"`
	Type           string             `json:"type"`
	Description    string             `json:"description"`
	DERControlBase DERControlBaseView `json:"derControlBase"`
	Functions      FunctionsView      `json:"functions"`
	CreationTime   int64              `json:"creationTime"`
	Interval       IntervalView       `json:"interval"`
	Ended          bool               `json:"ended"`
	// Origin is "admin" or "derms"; a record saved before the field reads admin.
	Origin string `json:"origin"`
	// Direction is "discharge" or "charge" from the sign of opModTargetW, or null.
	Direction    *string              `json:"direction"`
	Cancelled    bool                 `json:"cancelled"`
	CancelledAt  *int64               `json:"cancelledAt"`
	CancelReason string               `json:"cancelReason,omitempty"`
	Copies       []FSAControlCopyView `json:"copies"`
}

// FSAControlCreated is the 201 body of a create.
type FSAControlCreated struct {
	FSAControlView
	NotificationAttempted bool `json:"notificationAttempted"`
	Persisted             bool `json:"persisted"`
}

// FSAControlList is the list body. Controls is always an array, oldest
// creationTime first.
type FSAControlList struct {
	FSA      string           `json:"fsa"`
	Program  string           `json:"program"`
	Controls []FSAControlView `json:"controls"`
}

var refuseCopyMissing = derControlRefusal{http.StatusConflict, "copy_missing", "a device assigned to this fsa holds no copy of the program"}

const fsaControlOp = "fsa_control"

// wired reports whether every dependency is set, answering 500 when not.
func (h *AdminFSAControlHandler) wired(w http.ResponseWriter, r *http.Request) bool {
	fh := h.FSAs
	if fh == nil || fh.AdminFSAs == nil || fh.Copies == nil || h.Controls == nil || h.Issuer == nil || h.Controls.Lifecycles == nil {
		writeError(w, http.StatusInternalServerError, templateInternalError)
		return false
	}
	return true
}

// HandleCreate returns the handler for POST .../controls.
func (h *AdminFSAControlHandler) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID, tmplID := r.PathValue("id"), r.PathValue("derpId")
		var logged derControlLog
		if !h.wired(w, r) {
			return
		}
		ch, fh := h.Controls, h.FSAs
		var body fsaControlBody
		if ref, ok := decodeDERControlBody(w, r, &body, false); !ok {
			ch.refuse(w, r, fsaControlOp, ref, &logged)
			return
		}
		href := dercontrol.UnscopedProgramHref
		req, ref, ok := buildCreateRequest(r.Context(), ch.Curves, derControlCreateBody{
			controlValuesBody: body.controlValuesBody,
			DERProgramHref:    &href, StartTime: body.StartTime, DurationSeconds: body.DurationSeconds, Description: body.Description,
		}, &logged)
		if !ok {
			ch.refuse(w, r, fsaControlOp, ref, &logged)
			return
		}
		if err := ch.Issuer.Validate(req); err != nil {
			ch.refuse(w, r, fsaControlOp, mapIssuerError(err, &logged), &logged)
			return
		}
		if fh.deviceOnlyRefused(w, fsaID) {
			return
		}

		// Held through the write, so no device is assigned or unassigned
		// between reading the FSA's devices and writing their copies.
		fh.programMu.Lock()
		defer fh.programMu.Unlock()
		ctx := r.Context()
		t, ok := fh.lookupTemplate(w, r, fsaID, tmplID)
		if !ok {
			return
		}
		devices := fh.AdminFSAs.Devices(ctx, fsaID)
		for _, dev := range devices {
			present, err := fh.Copies.Check(ctx, dev, fsaID, t)
			if err == nil && !present {
				logged.add("device_id", dev)
				ch.refuse(w, r, fsaControlOp, refuseCopyMissing, &logged)
				return
			}
			if err != nil {
				fh.templateFailed(w, r, "copy_check", err)
				return
			}
		}

		record := h.recorder(fsaID, t.ID, req.Origin)
		routeHref := "/api/fsas/" + fsaID + "/der-programs/" + t.ID + "/controls"
		res, err := h.issueAcross(ctx, req, devices, fsaID, t.ID, record)
		var undo *dercontrol.UndoError
		if errors.As(err, &undo) && undo.Step == dercontrol.UndoStepStoreRecord {
			h.recordKept(w, r, routeHref, undo)
			return
		}
		if err != nil {
			ch.issueFailed(w, r, fsaControlOp, err, "", routeHref, &logged)
			return
		}

		for _, c := range res.Copies {
			ch.notify(ctx, c.Scope)
		}
		rec, err := fh.AdminFSAs.FSAControl(ctx, fsaID, t.ID, res.ID)
		if err != nil {
			fh.templateFailed(w, r, "record_read", err)
			return
		}
		var audit derControlLog
		addBaseToLog(&audit, res.Control)
		ch.log(r, slog.LevelInfo, "admin: FSA DER control created", "der_fsa_control_created", append(audit.attrs,
			"mrid", rec.MRID, "fsa", fsaID, "template", t.ID, "copies", len(res.Copies)))
		writeJSON(w, http.StatusCreated, FSAControlCreated{
			FSAControlView:        h.view(ctx, fsaID, rec, devices, sep2time.Now().Unix()),
			NotificationAttempted: ch.Notifier != nil,
			Persisted:             ch.Persisted && fh.AdminFSAs.Persists(),
		})
	}
}

// recorder is the RecordWriter for a control on fsaID's template tmplID: it
// stores the FSA's record, marked derms when origin is dercontrol.OriginDERMS,
// and returns how to remove it. IssueAcross owns the origin check and runs it
// before it calls the writer, so a value it refused never reaches here.
func (h *AdminFSAControlHandler) recorder(fsaID, tmplID, origin string) dercontrol.RecordWriter {
	fh := h.FSAs
	return func(ctx context.Context, id string, ctrl sep2.DERControl) (func(context.Context) error, error) {
		rec := recordOf(tmplID, id, ctrl)
		if origin == dercontrol.OriginDERMS {
			rec.Origin = origin
		}
		if err := fh.AdminFSAs.CreateFSAControl(ctx, fsaID, rec); err != nil {
			return nil, err
		}
		return func(ctx context.Context) error {
			err := fh.AdminFSAs.DeleteFSAControl(ctx, fsaID, tmplID, id)
			if err != nil {
				// Reported as failed, so no device assigned later may get it.
				fh.AdminFSAs.HoldFSAControl(fsaID, tmplID, id)
			}
			return err
		}, nil
	}
}

// issueAcross runs the fan-out to every device's copy inside the commitment
// lock of each device's fleet. With no device assigned there is no fleet to
// check, and only the record is written.
func (h *AdminFSAControlHandler) issueAcross(ctx context.Context, req dercontrol.CreateRequest, devices []string, fsaID, tmplID string, record dercontrol.RecordWriter) (dercontrol.AcrossResult, error) {
	if len(devices) == 0 {
		return h.Issuer.IssueAcross(ctx, req, nil, record)
	}
	fleets, keys, err := h.fleetsOf(ctx, devices)
	if err != nil {
		return dercontrol.AcrossResult{}, err
	}
	var res dercontrol.AcrossResult
	err = h.Controls.withinFleets(ctx, keys, func(v commitment.View) error {
		targets := make([]dercontrol.Target, 0, len(devices))
		for _, dev := range devices {
			targets = append(targets, dercontrol.Target{
				Scope: dercontrol.Scope{EndDeviceID: dev, FSAID: fsaID, DERProgramID: tmplID},
				Fleet: dercontrol.Fleet{Key: fleets[dev], Reach: controlReach, Check: checkIn(v)},
			})
		}
		var err error
		res, err = h.Issuer.IssueAcross(ctx, req, targets, record)
		return err
	})
	return res, err
}

// fleetsOf resolves each device's fleet, returning the map and the distinct
// keys.
func (h *AdminFSAControlHandler) fleetsOf(ctx context.Context, devices []string) (map[string]string, []string, error) {
	fleets := make(map[string]string, len(devices))
	var keys []string
	for _, dev := range devices {
		key, err := h.Controls.fleetOf(ctx, dev)
		if err != nil {
			return nil, nil, err
		}
		fleets[dev] = key
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return fleets, slices.Compact(keys), nil
}

// HandleList returns the handler for GET .../controls.
func (h *AdminFSAControlHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID, tmplID := r.PathValue("id"), r.PathValue("derpId")
		if !h.wired(w, r) {
			return
		}
		ctx := r.Context()
		t, ok := h.FSAs.lookupTemplate(w, r, fsaID, tmplID)
		if !ok {
			return
		}
		devices := h.FSAs.AdminFSAs.Devices(ctx, fsaID)
		now := sep2time.Now().Unix()
		out := FSAControlList{FSA: fsaID, Program: t.ID, Controls: []FSAControlView{}}
		for _, rec := range h.FSAs.AdminFSAs.FSAControls(ctx, fsaID, t.ID) {
			out.Controls = append(out.Controls, h.view(ctx, fsaID, rec, devices, now))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// HandleCancel returns the handler for POST .../controls/{controlId}/cancel.
// Every copy is cancelled before the record, so a cancel that stops partway
// leaves the record live and a retry finishes it.
func (h *AdminFSAControlHandler) HandleCancel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fsaID, tmplID, id := r.PathValue("id"), r.PathValue("derpId"), r.PathValue("controlId")
		var logged derControlLog
		if !h.wired(w, r) {
			return
		}
		ch, fh := h.Controls, h.FSAs
		var body derControlCancelBody
		if ref, ok := decodeDERControlBody(w, r, &body, true); !ok {
			ch.refuse(w, r, "fsa_cancel", ref, &logged)
			return
		}
		reason := ""
		if body.Reason != nil {
			reason = *body.Reason
		}
		if !validString(reason, maxCancelReasonOctets) {
			ch.refuse(w, r, "fsa_cancel", refuseReason, &logged)
			return
		}

		// Held so no device is assigned, and handed a live replica, while
		// the copies are being cancelled.
		fh.programMu.Lock()
		defer fh.programMu.Unlock()
		ctx := r.Context()
		t, ok := fh.lookupTemplate(w, r, fsaID, tmplID)
		if !ok {
			return
		}
		rec, err := fh.AdminFSAs.FSAControl(ctx, fsaID, t.ID, id)
		if errors.Is(err, store.ErrNotFound) {
			ch.refuse(w, r, "fsa_cancel", refuseControlNotFound, &logged)
			return
		}
		if err != nil {
			fh.templateFailed(w, r, "record_read", err)
			return
		}
		logged.add("mrid", rec.MRID)
		now := sep2time.Now().Unix()
		switch {
		case rec.CancelledAt != nil:
			ch.refuse(w, r, "fsa_cancel", refuseAlreadyCancelled, &logged)
			return
		case rec.Ended(now):
			ch.refuse(w, r, "fsa_cancel", refuseEnded, &logged)
			return
		}

		devices := fh.AdminFSAs.Devices(ctx, fsaID)
		var cancelled []dercontrol.Scope
		for _, dev := range devices {
			scope := dercontrol.Scope{EndDeviceID: dev, FSAID: fsaID, DERProgramID: t.ID}
			_, err := ch.cancelInFleet(ctx, scope, rec.ID, rec.MRID, reason)
			var refusal *dercontrol.RefusalError
			if errors.As(err, &refusal) {
				switch refusal.Code {
				case dercontrol.RefusalAlreadyCancelled, dercontrol.RefusalAlreadySuperseded, dercontrol.RefusalEnded, dercontrol.RefusalControlNotFound:
					continue
				}
			}
			if err != nil {
				for _, s := range cancelled {
					ch.notify(ctx, s)
				}
				h.cancelStopped(w, r, err, dev, rec, &logged)
				return
			}
			cancelled = append(cancelled, scope)
		}
		for _, s := range cancelled {
			ch.notify(ctx, s)
		}

		at := now
		rec.CancelledAt, rec.CancelReason = &at, reason
		if err := fh.AdminFSAs.UpdateFSAControl(ctx, fsaID, rec); err != nil {
			// Every copy is cancelled, so no device assigned later may get it
			// as live while the record says otherwise.
			fh.AdminFSAs.HoldFSAControl(fsaID, t.ID, rec.ID)
			logged.add("cause", "record_cancel_failed")
			logRefusal(ch.logger(), r, refusedEvent("fsa_cancel"), refuseInternal, &logged)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf(
				"every copy of control %s is cancelled, but its record could not be written; it is not sent to devices assigned meanwhile, so retry the cancel", rec.MRID))
			return
		}
		ch.log(r, slog.LevelInfo, "admin: FSA DER control cancelled", "der_fsa_control_cancelled", []any{
			"mrid", rec.MRID, "fsa", fsaID, "template", t.ID, "copies", len(cancelled)})
		writeJSON(w, http.StatusOK, h.view(ctx, fsaID, rec, devices, sep2time.Now().Unix()))
	}
}

// recordKept answers a fan-out that failed and removed every copy but not its
// record. The record is held from replication until the next restart, which
// replicates it unless it is cancelled first.
func (h *AdminFSAControlHandler) recordKept(w http.ResponseWriter, r *http.Request, routeHref string, undo *dercontrol.UndoError) {
	cancelHref := routeHref + "/" + undo.ID + "/cancel"
	h.Controls.log(r, slog.LevelError, "admin: FSA DER control record left after a failed create", "der_fsa_control_record_kept", []any{
		"code", refuseInternal.code, "mrid", undo.MRID, "record", routeHref + "/" + undo.ID, "error", undo,
	})
	writeJSON(w, http.StatusInternalServerError, DERControlIncomplete{
		Error: "the create failed and no device holds the control, but its record could not be removed; it is not sent to devices assigned before the next restart, " +
			"and is sent at that restart unless cancelled with POST " + cancelHref,
		MRID: undo.MRID,
		Href: routeHref + "/" + undo.ID,
	})
}

// cancelStopped answers a cancel that failed at dev's copy. The copies before
// it stay cancelled and the record stays live, so a retry completes it.
func (h *AdminFSAControlHandler) cancelStopped(w http.ResponseWriter, r *http.Request, err error, dev string, rec memory.FSAControl, logged *derControlLog) {
	ch := h.Controls
	if isContextErr(err) && r.Context().Err() != nil {
		logged.add("cause", "client_gone")
		ch.refuse(w, r, "fsa_cancel", refuseClientGone, logged)
		return
	}
	var undo *dercontrol.UndoError
	if errors.As(err, &undo) {
		h.Controls.incomplete(w, r, "fsa_cancel", "cancellation may be recorded: its write could not be undone", undo)
		return
	}
	logged.add("device_id", dev)
	logRefusal(ch.logger(), r, refusedEvent("fsa_cancel"), refuseInternal, logged)
	writeError(w, http.StatusInternalServerError, fmt.Sprintf(
		"cancel stopped at device %s; the copies before it are cancelled and control %s is still live, so retry the cancel", dev, rec.MRID))
}

// view is rec with the copy held by each of devices.
func (h *AdminFSAControlHandler) view(ctx context.Context, fsaID string, rec memory.FSAControl, devices []string, now int64) FSAControlView {
	base := rec.Base.Copy()
	typ, baseView, fns := controlBaseViews(&base)
	v := FSAControlView{
		ID:             rec.ID,
		MRID:           rec.MRID,
		Type:           typ,
		Description:    rec.Description,
		DERControlBase: baseView,
		Functions:      fns,
		CreationTime:   rec.CreationTime,
		Interval:       IntervalView{Start: rec.Start, Duration: rec.Duration},
		Ended:          rec.Ended(now),
		Origin:         dercontrol.OriginOf(rec.Origin),
		Direction:      targetDirectionOf(dercontrol.TargetOf(&rec.Base)),
		Cancelled:      rec.CancelledAt != nil,
		CancelledAt:    rec.CancelledAt,
		CancelReason:   rec.CancelReason,
		Copies:         []FSAControlCopyView{},
	}
	for _, dev := range devices {
		scope := dercontrol.Scope{EndDeviceID: dev, FSAID: fsaID, DERProgramID: rec.TemplateID}
		cv := FSAControlCopyView{Device: dev, Href: scope.ControlHref(rec.ID)}
		if lc, err := h.Controls.Lifecycles.Get(ctx, scope.Key(), rec.ID); err == nil {
			st := dercontrol.DeriveStatus(now, rec.CreationTime, rec.Start, lc)
			cv.EventStatus = &EventStatusView{CurrentStatus: st.CurrentStatus, Status: eventStatusName(st.CurrentStatus), DateTime: st.DateTime}
		}
		v.Copies = append(v.Copies, cv)
	}
	return v
}

// recordOf is the FSA's record of ctrl, a control IssueAcross is writing.
func recordOf(tmplID, id string, ctrl sep2.DERControl) memory.FSAControl {
	rec := memory.FSAControl{
		TemplateID:   tmplID,
		ID:           id,
		MRID:         ctrl.MRID,
		CreationTime: ctrl.CreationTime,
		Description:  ctrl.Description,
	}
	if ctrl.Interval != nil {
		rec.Start, rec.Duration = ctrl.Interval.Start, ctrl.Interval.Duration
	}
	if ctrl.DERControlBase != nil {
		rec.Base = ctrl.DERControlBase.Copy()
	}
	return rec
}

// controlOf is the control rec's copies carry, without an href.
func controlOf(rec memory.FSAControl) sep2.DERControl {
	base := rec.Base.Copy()
	c := sep2.DERControl{DERControlBase: &base}
	c.MRID = rec.MRID
	c.Description = rec.Description
	c.CreationTime = rec.CreationTime
	c.Interval = &sep2.DateTimeInterval{Start: rec.Start, Duration: rec.Duration}
	return c
}

// ReplicateTo writes every live control of fsaID's templates, neither
// cancelled nor ended, to deviceID's copies, oldest creationTime first,
// inside the commitment lock of the device's fleet. A copy already holding
// one keeps it. The error contract is dercontrol.Issuer.Replicate's, plus a
// *commitment.ConflictError from the device's fleet.
func (h *AdminFSAControlHandler) ReplicateTo(ctx context.Context, deviceID, fsaID string) error {
	replicas := liveReplicas(ctx, h.FSAs.AdminFSAs, deviceID, fsaID, sep2time.Now().Unix())
	replicas, err := h.heldCopies(ctx, deviceID, fsaID, replicas)
	if err != nil || len(replicas) == 0 {
		return err
	}
	if h.Issuer == nil || h.Controls == nil {
		return errors.New("fsa controls cannot be replicated: no issuer wired")
	}
	key, err := h.Controls.fleetOf(ctx, deviceID)
	if err != nil {
		return err
	}
	return h.Controls.withinFleets(ctx, []string{key}, func(v commitment.View) error {
		for i := range replicas {
			replicas[i].Target.Fleet = dercontrol.Fleet{Key: key, Reach: controlReach, Check: checkIn(v)}
		}
		_, err := h.Issuer.Replicate(ctx, replicas)
		return err
	})
}

// heldCopies keeps the replicas whose template deviceID holds a copy of. The
// boot rebuild skips a copy that conflicts with a program the device already
// holds, and its controls are skipped with it, logged once per template.
func (h *AdminFSAControlHandler) heldCopies(ctx context.Context, deviceID, fsaID string, replicas []dercontrol.Replica) ([]dercontrol.Replica, error) {
	if h.FSAs == nil || h.FSAs.Copies == nil {
		return replicas, nil
	}
	held := map[string]bool{}
	out := replicas[:0:0]
	for _, rep := range replicas {
		tmplID := rep.Target.Scope.DERProgramID
		ok, seen := held[tmplID]
		if !seen {
			t, err := h.FSAs.AdminFSAs.Template(ctx, fsaID, tmplID)
			if err != nil {
				return nil, fmt.Errorf("reading template %s of fsa %s: %w", tmplID, fsaID, err)
			}
			present, err := h.FSAs.Copies.Check(ctx, deviceID, fsaID, t)
			switch {
			case errors.Is(err, fsaprogram.ErrCopyConflict):
			case err != nil:
				return nil, err
			}
			ok = present
			held[tmplID] = ok
			if !ok {
				h.FSAs.logged(nil).WarnContext(ctx, "admin: FSA controls not replicated to a device without the copy", "event", "fsa_control_replica_skipped",
					"device", deviceID, "fsa", fsaID, "template", tmplID, "reason", fmt.Sprint(err))
			}
		}
		if ok {
			out = append(out, rep)
		}
	}
	return out, nil
}

// liveReplicas lists the controls of fsaID's templates still to be carried
// out at now, as replicas into deviceID's copies, each template's oldest
// first.
func liveReplicas(ctx context.Context, admin *memory.AdminFSAStore, deviceID, fsaID string, now int64) []dercontrol.Replica {
	var out []dercontrol.Replica
	for _, t := range admin.Templates(ctx, fsaID) {
		for _, rec := range admin.FSAControls(ctx, fsaID, t.ID) {
			if rec.CancelledAt != nil || rec.Held || rec.Ended(now) {
				continue
			}
			out = append(out, dercontrol.Replica{
				Target:  dercontrol.Target{Scope: dercontrol.Scope{EndDeviceID: deviceID, FSAID: fsaID, DERProgramID: t.ID}},
				ID:      rec.ID,
				Control: controlOf(rec),
				Origin:  rec.Origin,
			})
		}
	}
	return out
}
