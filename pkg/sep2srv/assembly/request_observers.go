package assembly

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/statuswriter"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Caps on the request text a device log line carries. A path is the one
// value a client chooses freely, so it and the other client-chosen strings
// are bounded here.
const (
	maxLoggedPath   = 256
	maxLoggedMethod = 16
	maxLoggedEdevID = 64
)

// requestObservers reads the caller's identity once per request that passed
// Wrap and feeds every observer from that one reading: the activity recorder
// and the device log. Each observer is optional.
type requestObservers struct {
	identity func(ctx context.Context) (lfdi, sfdi string, ok bool)
	activity *activity.Recorder
	log      *devicelog.Writer
	devices  store.EndDeviceReader
	managers store.EndDeviceManagementReader
	now      func() time.Time
}

// wrap returns next unchanged when there is no observer, so a router built
// without them pays nothing per request. Place it inside Wrap: a request the
// identity or ACL layer refused never reaches it.
func (o requestObservers) wrap(next http.Handler) http.Handler {
	if o.activity == nil && o.log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lfdi, _, ok := o.identity(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		o.activity.Record(lfdi)
		if o.log == nil {
			next.ServeHTTP(w, r)
			return
		}
		rec := statuswriter.New(w)
		start := o.now()
		next.ServeHTTP(rec, r)
		o.logRequest(r, lfdi, rec, o.now().Sub(start))
	})
}

// logRequest hands one line to the device log. It runs after the handler
// has returned, never reads the request body, does no store read, and never
// fails the request: a full queue drops and counts the line inside the Writer.
func (o requestObservers) logRequest(r *http.Request, lfdi string, rec *statuswriter.Recorder, dur time.Duration) {
	actor, ok := sep2lfdi.Canonical(lfdi)
	if !ok {
		// A certificate identity is always an LFDI; anything else has no
		// file to key on.
		return
	}
	fields := map[string]any{
		"method":     capString(r.Method, maxLoggedMethod),
		"path":       capString(r.URL.Path, maxLoggedPath),
		"status":     rec.Status,
		"dur_ms":     float64(dur.Microseconds()) / 1000,
		"req_bytes":  max(r.ContentLength, 0),
		"resp_bytes": rec.Bytes,
		"remote":     r.RemoteAddr,
	}
	edev := edevID(r.URL.Path)
	if edev != "" {
		fields["edev"] = edev
	}
	// The store reads for target and registration happen in the writer
	// goroutine, so a slow store delays the log and never this request. The
	// Writer counts and logs every drop, so the error carries nothing the
	// request path could act on.
	_ = o.log.AppendResolved(actor, edev, o.resolve, devicelog.Entry{Kind: "request", Fields: fields})
}

// resolve runs in the device log's writer goroutine. It reports whether
// actor has an EndDevice record or a management pair, and the canonical LFDI
// of the EndDevice edev names ("" when it does not resolve). A store error
// counts as not registered or no target: the shared file is the bounded place
// for a line whose owner cannot be established.
func (o requestObservers) resolve(ctx context.Context, actor, edev string) (registered bool, target string) {
	if edev != "" && o.devices != nil {
		if dev, err := o.devices.Get(ctx, edev); err == nil {
			target, _ = sep2lfdi.Canonical(dev.LFDI)
		}
	}
	if o.devices != nil {
		if _, err := o.devices.GetByLFDI(ctx, actor); err == nil {
			return true, target
		}
	}
	if o.managers != nil {
		if managed, err := o.managers.ManagedBy(ctx, actor); err == nil && len(managed) > 0 {
			return true, target
		}
		if _, err := o.managers.ManagerOf(ctx, actor); err == nil {
			return true, target
		}
	}
	return false, target
}

// edevID returns the EndDevice id of an /edev/{id}/... path, or "" for any
// other path or an id longer than maxLoggedEdevID. The mux has not run yet,
// so r.PathValue is not available here.
func edevID(path string) string {
	rest, ok := strings.CutPrefix(path, "/edev/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if len(id) > maxLoggedEdevID {
		return ""
	}
	return id
}

// capString cuts s to at most n bytes without splitting a UTF-8 sequence.
func capString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
