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
// A source must honor ctx: the server bounds each pass and stops waiting at
// the deadline, but a call that ignores ctx keeps running until it returns.
// A source that returns an error, panics or overruns the deadline never
// removes a row; its columns show "-" in every cell and carry the error.
type DeviceColumnSource interface {
	// Columns lists the columns this source supplies, in display order. It
	// is read on every pass and must not block.
	Columns() []DeviceColumn
	// Cells returns text per LFDI and column ID. A missing LFDI or column
	// shows "-".
	Cells(ctx context.Context, lfdis []string) (map[string]map[string]string, error)
}
