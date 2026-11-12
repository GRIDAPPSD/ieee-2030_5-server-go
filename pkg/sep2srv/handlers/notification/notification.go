// Package notification serves POST /ntfy, where a device delivers a
// Notification to the server.
package notification

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/reqlimit"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
)

// Href is the path the notification endpoint is served at.
const Href = "/ntfy"

// Recorder stores an accepted Notification; devicelog.NotificationRecorder is
// the server's.
type Recorder interface {
	Record(ctx context.Context, n sep2.Notification) error
}

// maxStatus is the highest Notification status the standard defines; larger
// values are reserved.
const maxStatus = sep2.NotificationStatusCanceledDeleted

// HandlePost returns the handler for POST /ntfy (WADL mode M, 200 or 201
// with a Location header). The server keeps no Notification resource, so the
// device log line is the only record and the Location is the endpoint itself.
// A 200 means the line was queued for writing, not that it reached disk.
//
// A device the recorder does not know is refused with 403, the status the
// ACL gives a caller whose credentials a path does not accept. A nil
// recorder (device logging off) answers 503 rather than accept a body it
// would drop.
func HandlePost(rec Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rec == nil {
			http.Error(w, "notifications cannot be recorded", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, reqlimit.MaxBody))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		var n sep2.Notification
		if err := xml.Unmarshal(body, &n); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}
		if err := validate(n); err != nil {
			srverr.BadRequestMessage(w, r, err.Error(), err)
			return
		}
		if err := rec.Record(r.Context(), n); err != nil {
			if errors.Is(err, devicelog.ErrUnknownDevice) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		w.Header().Set("Location", Href)
		w.WriteHeader(http.StatusOK)
	}
}

// validate rejects a Notification whose status the standard reserves. The
// required elements are not checked for presence, as for every other POST in
// this server: an element-only document is accepted and logged as sent.
func validate(n sep2.Notification) error {
	if n.Status > maxStatus {
		return errors.New("status is reserved")
	}
	return nil
}
