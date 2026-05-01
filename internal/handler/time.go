package handler

import (
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// HandleTime returns a handler for GET /tm.
// Time is a mandatory function set per spec section 9.2.
func HandleTime(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		now := time.Now()
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
