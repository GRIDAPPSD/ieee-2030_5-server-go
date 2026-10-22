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

// refusalKey carries a *refusal in the request context.
type refusalKey struct{}

// refusal is what a gate that answers 403 tells the request observer. The
// reason is one of the gate's fixed reason constants.
type refusal struct{ reason string }

// noteRefusal records reason for the request observer. It is a no-op when no
// observer put a refusal in ctx, so a gate built without one behaves as before.
func noteRefusal(ctx context.Context, reason string) {
	if ref, ok := ctx.Value(refusalKey{}).(*refusal); ok {
		ref.reason = reason
	}
}

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
	r := devicelog.NewResolver(devicelog.ResolverConfig{Registered: l.registered, Target: l.target})
	w.AddStat("lookups refused at the in-flight limit", r.Refused)
	return r
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
		// The ownership gate reports a 403 through this value, so the line
		// for a refused request says why without the observer re-deciding.
		ref := &refusal{}
		r = r.WithContext(context.WithValue(r.Context(), refusalKey{}, ref))
		start := o.now()
		// A handler that panics still made a request: write its line as a
		// 500 and re-panic, so the server's recovery behaves as before.
		defer func() {
			if p := recover(); p != nil {
				rec.Status = http.StatusInternalServerError
				o.logRequest(r, lfdi, rec, ref, o.now().Sub(start))
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r)
		o.logRequest(r, lfdi, rec, ref, o.now().Sub(start))
	})
}

// logRequest hands one line to the device log. It runs after the handler
// has returned, never reads the request body, does no store read, and never
// fails the request: a full queue drops and counts the line inside the Writer.
func (o requestObservers) logRequest(r *http.Request, lfdi string, rec *statuswriter.Recorder, ref *refusal, dur time.Duration) {
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
	kind := "request"
	if ref.reason != "" && rec.Status == http.StatusForbidden {
		// A refusal replaces the request line rather than adding a second
		// one, and carries the gate's fixed reason text, never client text.
		kind = devicelog.KindRefused
		fields = map[string]any{
			"method": fields["method"],
			"path":   fields["path"],
			"status": rec.Status,
			"reason": ref.reason,
			"remote": r.RemoteAddr,
		}
	}
	edev := edevID(r.URL.Path)
	if edev != "" {
		fields["edev"] = edev
	}
	// The store reads for target and registration happen in the writer
	// goroutine, so a slow store delays the log and never this request. The
	// Writer counts and logs every drop, so the error carries nothing the
	// request path could act on.
	_ = o.log.AppendResolved(actor, edev, o.resolver.Resolve, devicelog.Entry{Kind: kind, Fields: fields})
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
