package sep2time

import (
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
)

// nowFunc is the wall-clock source for the Time resource. It defaults to
// time.Now in all production builds and is bit-identical to a direct
// time.Now() call. The sibling file time_test_hook.go (built only with
// the csip_test_hooks build tag) reassigns nowFunc at init() to add a
// test-controlled offset, enabling CSIP V1.2 CORE-006 (TM_TIME_ADJUSTED),
// per GRIDAPPSD/ieee-2030_5-server-go#27 and #28.
var nowFunc = time.Now

// Now returns the current time from the same clock HandleTime serves,
// including the csip_test_hooks build's AdvanceClock offset. Exported so
// other packages can share the one clock the server presents to a device
// via /tm, rather than reading time.Now() directly and drifting from it
// under the test hook (#563).
func Now() time.Time {
	return nowFunc()
}

// TimeParams holds the time-resource configuration values supplied by the
// server at construction. Callers build this from their config struct; the
// handler closes over it so core never imports the server-side config
// package (callback-injection pattern from Phase D3).
type TimeParams struct {
	TZOffset    int32
	DSTOffset   int32
	DSTStart    int64
	DSTEnd      int64
	TimeQuality uint8
}

// HandleTime returns a handler for GET /tm.
// Time is a mandatory function set per spec section 9.2.
func HandleTime(params TimeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		now := nowFunc()
		tm := sep2.Time{
			Resource:     sep2.Resource{Href: "/tm"},
			CurrentTime:  now.Unix(),
			DstEndTime:   params.DSTEnd,
			DstOffset:    params.DSTOffset,
			DstStartTime: params.DSTStart,
			Quality:      params.TimeQuality,
			TzOffset:     params.TZOffset,
		}

		encoding.WriteXML(w, http.StatusOK, &tm)
	}
}
