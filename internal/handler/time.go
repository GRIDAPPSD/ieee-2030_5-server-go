package handler

import (
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// nowFunc is the wall-clock source for the Time resource. It defaults to
// time.Now in all production builds and is bit-identical to a direct
// time.Now() call. The sibling file time_test_hook.go (built only with
// the csip_test_hooks build tag) reassigns nowFunc at init() to add a
// test-controlled offset, enabling CSIP V1.2 CORE-006 (TM_TIME_ADJUSTED).
// See IEEE-024 / IEEE-025.
var nowFunc = time.Now

// HandleTime returns a handler for GET /tm.
// Time is a mandatory function set per spec section 9.2.
func HandleTime(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		now := nowFunc()
		tm := sep2.Time{
			Resource:     sep2.Resource{Href: "/tm"},
			CurrentTime:  now.Unix(),
			DstEndTime:   cfg.DSTEnd,
			DstOffset:    cfg.DSTOffset,
			DstStartTime: cfg.DSTStart,
			Quality:      cfg.TimeQuality,
			TzOffset:     cfg.TZOffset,
		}

		encoding.WriteXML(w, http.StatusOK, &tm)
	}
}
