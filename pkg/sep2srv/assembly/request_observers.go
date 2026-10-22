package assembly

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/statuswriter"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
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
	identity identity.Func
	activity *activity.Recorder
	log      *devicelog.Writer
	resolver *devicelog.CachingResolver
	now      func() time.Time
}

// deviceLogLookups are the store reads behind the device log's choice of
// file and target, shared by the request observer and the LogEvent decorator
// so one resolver, with one cache, serves both.
type deviceLogLookups struct {
	devices  store.EndDeviceReader
	managers store.EndDeviceManagementReader
}

// newDeviceLogResolver returns the resolver for w over stores, or nil when
// logging is off.
func newDeviceLogResolver(w *devicelog.Writer, stores *Stores) *devicelog.CachingResolver {
	if w == nil {
		return nil
	}
	var l deviceLogLookups
	if stores != nil {
		l = deviceLogLookups{devices: stores.EndDevices, managers: stores.EndDeviceManagers}
	}
	return devicelog.NewResolver(devicelog.ResolverConfig{Registered: l.registered, Target: l.target})
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
		"method":     logsafe.TruncateOctets(r.Method, maxLoggedMethod),
		"path":       logsafe.TruncateOctets(r.URL.Path, maxLoggedPath),
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
	_ = o.log.AppendResolved(actor, edev, o.resolver.Resolve, devicelog.Entry{Kind: "request", Fields: fields})
}

// registered reports whether actor has an EndDevice record or a management
// pair. Not found is a definite no; any other store error is returned so the
// writer keeps the line in the actor's own file and counts it.
func (o deviceLogLookups) registered(ctx context.Context, actor string) (bool, error) {
	if o.devices != nil {
		_, err := o.devices.GetByLFDI(ctx, actor)
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, store.ErrNotFound):
			return false, err
		}
	}
	if o.managers != nil {
		managed, err := o.managers.ManagedBy(ctx, actor)
		if err != nil {
			return false, err
		}
		if len(managed) > 0 {
			return true, nil
		}
		if _, err := o.managers.ManagerOf(ctx, actor); err == nil {
			return true, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
	}
	return false, nil
}

// target returns the canonical LFDI of the EndDevice edev names, "" when no
// such device exists, and an error when the store could not say.
func (o deviceLogLookups) target(ctx context.Context, edev string) (string, error) {
	if o.devices == nil {
		return "", nil
	}
	dev, err := o.devices.Get(ctx, edev)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	target, _ := sep2lfdi.Canonical(dev.LFDI)
	return target, nil
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
