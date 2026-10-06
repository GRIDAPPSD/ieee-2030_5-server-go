package sep2admin

import "context"

// DeviceColumn names one extra column an embedder adds to the Devices tab.
// ID is the key its cells are returned under and the key the payload carries
// them under, so it must be stable across calls; Label is the header text.
type DeviceColumn struct {
	ID    string
	Label string
}

// DeviceColumnSource lets an embedder add columns to the Devices tab, one
// cell per registered EndDevice. The server passes every device's LFDI in one
// call, so a source answers from one read instead of one per row.
//
// Each source is bounded on its own and the sources run concurrently. A
// source must honor ctx: the server stops waiting at the deadline, but a call
// that ignores ctx keeps running until it returns, and no second call to that
// source starts meanwhile. Passes that arrive while a call is younger than
// its bound share its result; once it is older, they show the source's last
// good cells with a "still busy" error naming how long the call has run. A source that returns an error, panics or overruns the
// deadline never removes a row; cells it returned with an error are
// discarded, so its columns show "-" in every cell and carry the error.
// Column IDs must match [a-z0-9_-]{1,64} and be unique across sources; a
// refused column is dropped and the reason shows on the source's columns.
type DeviceColumnSource interface {
	// Columns lists the columns this source supplies, in display order. It
	// is called on every pass, inside the same bounded call as Cells.
	Columns() []DeviceColumn
	// Cells returns text per LFDI and column ID. A missing LFDI or column
	// shows "-".
	Cells(ctx context.Context, lfdis []string) (map[string]map[string]string, error)
}
