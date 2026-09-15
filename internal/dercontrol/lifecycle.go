package dercontrol

// LifecycleRecord is the server-owned companion to an admin-issued
// DERControl, keyed identically (the same scope and id as the control).
// It carries the state IEEE 2030.5 forbids storing on the DERControl
// itself: cancellation and supersede are status changes, and a stored
// Event is never edited (2018 line 5470).
//
// Serve-time EventStatus derivation from this record is a later issue;
// here it backs only Issue's supersede check and Cancel's refusals.
type LifecycleRecord struct {
	// CancelledAt is the Unix-second time Cancel was called, or nil.
	CancelledAt  *int64
	CancelReason string

	// SupersededAt is the Unix-second time (the superseding control's
	// interval start) at which this control is superseded, or nil.
	SupersededAt *int64
	SupersededBy string // mRID of the superseding control
}

// Copy returns an independent copy, satisfying store.Copier for use with a
// generic ScopedStore.
func (r LifecycleRecord) Copy() LifecycleRecord {
	c := r
	if r.CancelledAt != nil {
		v := *r.CancelledAt
		c.CancelledAt = &v
	}
	if r.SupersededAt != nil {
		v := *r.SupersededAt
		c.SupersededAt = &v
	}
	return c
}

// cancelled reports whether Cancel has been recorded.
func (r LifecycleRecord) cancelled() bool {
	return r.CancelledAt != nil
}

// supersedeEligible reports whether a control with this record may still be
// superseded by a new one whose interval starts at newStart. A cancelled
// control is never eligible. An unsuperseded control is always eligible. An
// already-superseded control is eligible only when newStart is earlier than
// its recorded SupersededAt: IEEE 2030.5-2018 marks a control Superseded at
// the earliest Effective Start Time of any overlapping event, so a
// later-discovered earlier overlap must move the mark backward rather than
// be skipped. Used by Issue's overlap scan (acceptance criterion 7).
func (r LifecycleRecord) supersedeEligible(newStart int64) bool {
	if r.CancelledAt != nil {
		return false
	}
	return r.SupersededAt == nil || newStart < *r.SupersededAt
}

// supersededAsOf reports whether now has reached the recorded supersede
// time. Before that instant the control still reads Scheduled or Active; a
// cancel refusal for "already superseded" checks this, not merely that
// SupersededAt is set.
func (r LifecycleRecord) supersededAsOf(now int64) bool {
	return r.SupersededAt != nil && now >= *r.SupersededAt
}
