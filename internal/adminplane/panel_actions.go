package adminplane

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// Action rate: one a second for the whole plane, with a burst of five, so
// an operator's few quick clicks pass and a script or a stuck key does not
// drive the embedder.
const (
	actionBurst        = 5
	actionRefillPeriod = time.Second
)

// tokenBucket is the plane-wide action limit. It is a bucket rather than a
// last-call timestamp so the burst is real, and it holds no goroutine.
type tokenBucket struct {
	mu     sync.Mutex
	now    func() time.Time
	tokens float64
	last   time.Time
}

func newTokenBucket(now func() time.Time) *tokenBucket {
	return &tokenBucket{now: now, tokens: actionBurst, last: now()}
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.now()
	if el := t.Sub(b.last); el > 0 {
		b.tokens = min(actionBurst, b.tokens+float64(el)/float64(actionRefillPeriod))
		b.last = t
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ActionTracker counts the panel action Run calls still executing. A Run
// that ignores its context outlives the request that started it, so the
// count is the plane's own, not the request's.
type ActionTracker struct{ n atomic.Int64 }

// Running is how many Run calls have started and not yet returned.
func (t *ActionTracker) Running() int { return int(t.n.Load()) }

type actionFieldEntry struct {
	Name    string                    `json:"name"`
	Label   string                    `json:"label"`
	Kind    sep2admin.ActionFieldKind `json:"kind"`
	Min     *int64                    `json:"min,omitempty"`
	Max     *int64                    `json:"max,omitempty"`
	MaxLen  int                       `json:"maxLen,omitempty"`
	Choices []choiceEntry             `json:"choices,omitempty"`
}

type actionEntry struct {
	ID     string             `json:"id"`
	Label  string             `json:"label"`
	Fields []actionFieldEntry `json:"fields,omitempty"`
}

// actionEntries lists a panel's actions without their fields, for the
// panel list.
func actionEntries(p sep2admin.Panel) []actionEntry {
	if len(p.Actions) == 0 {
		return nil
	}
	out := make([]actionEntry, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, actionEntry{ID: a.ID, Label: a.Label})
	}
	return out
}

// handleActions answers GET /api/ui/panels/{id}/actions with
// {"actions":[{id,label,fields:[...]}]}, each choice field carrying its
// current choices. Resolving them calls the embedder, so it shares the
// panel's busy flag and timeout with View.
func (ps *panelSet) handleActions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := ps.lookup(r.PathValue("id"))
		if !ok || len(p.Actions) == 0 {
			writePanelError(w, http.StatusNotFound, "no such panel")
			return
		}
		if ps.stopping() {
			writePanelError(w, http.StatusServiceUnavailable, "plane is shutting down")
			return
		}
		if !ps.actionLimit.allow() {
			w.Header().Set("Retry-After", "1")
			writePanelError(w, http.StatusTooManyRequests, "too many panel actions")
			return
		}
		out := make([]actionEntry, 0, len(p.Actions))
		_, ok = ps.invoke(w, r, p, func(ctx context.Context) (sep2admin.Descriptor, error) {
			for _, a := range p.Actions {
				choices, err := a.ListChoices(ctx)
				if err != nil {
					return sep2admin.Descriptor{}, err
				}
				out = append(out, describeAction(a, choices))
			}
			return sep2admin.Descriptor{}, nil
		})
		if !ok {
			return
		}
		writePanelJSON(w, http.StatusOK, struct {
			Actions []actionEntry `json:"actions"`
		}{out})
	}
}

func describeAction(a sep2admin.Action, choices map[string][]sep2admin.Choice) actionEntry {
	e := actionEntry{ID: a.ID, Label: a.Label, Fields: make([]actionFieldEntry, 0, len(a.Fields))}
	for _, f := range a.Fields {
		fe := actionFieldEntry{Name: f.Name, Label: f.Label, Kind: f.Kind}
		switch f.Kind {
		case sep2admin.ActionInteger:
			lo, hi := f.Min, f.Max
			fe.Min, fe.Max = &lo, &hi
		case sep2admin.ActionText:
			fe.MaxLen = f.MaxLen
		case sep2admin.ActionChoice:
			fe.Choices = make([]choiceEntry, 0, len(choices[f.Name]))
			for _, c := range choices[f.Name] {
				fe.Choices = append(fe.Choices, choiceEntry{ID: c.ID, Label: c.Label})
			}
		}
		e.Fields = append(e.Fields, fe)
	}
	return e
}

func findAction(p sep2admin.Panel, id string) (sep2admin.Action, bool) {
	for _, a := range p.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return sep2admin.Action{}, false
}

const (
	actionInvalidRefusal  = "invalid action values"
	actionFailedMessage   = "action failed"
	actionUnknownOutcome  = "action did not answer in time; it may still complete, so do not retry blindly"
	actionCanceledOutcome = "request canceled; the action may still complete, so do not retry blindly"
)

// refuseAction answers a request the plane refused before Run, and leaves
// one audit line for it the way the cross-origin refusal does. The line
// names who and what, never a submitted value.
func refuseAction(w http.ResponseWriter, r *http.Request, p sep2admin.Panel, a sep2admin.Action, status int, reason, msg string) {
	log.Printf("admin: panel %q action %q: refused reason=%s status=%d admission=%s remote=%s",
		p.ID, a.ID, reason, status, auth.AdmissionPath(r), r.RemoteAddr)
	writePanelError(w, status, msg)
}

// auditAction writes one outcome line for an action that reached Run.
func auditAction(r *http.Request, p sep2admin.Panel, a sep2admin.Action, outcome string) {
	log.Printf("admin: panel %q action %q: outcome=%s admission=%s remote=%s", p.ID, a.ID, outcome, auth.AdmissionPath(r), r.RemoteAddr)
}

// panelActionsOff answers a POST to the action path with 404 while
// PanelActions is off. The route is not mounted then, and without this the
// dashboard's catch-all "GET /" makes net/http answer 405 for the path,
// which would tell a caller the route exists. It sits after the credential
// guard, so an uncredentialed caller still gets 401.
func panelActionsOff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "", "api", "ui", "panels", {id}, "actions", {action}
		parts := strings.Split(r.URL.Path, "/")
		if r.Method == http.MethodPost && len(parts) == 7 && parts[3] == "panels" && parts[5] == "actions" && parts[1] == "api" && parts[2] == "ui" {
			writePanelError(w, http.StatusNotFound, "panel actions are off")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleAction answers POST /api/ui/panels/{id}/actions/{action}. The checks
// run cheapest and most decisive first: the route exists, the credential is
// not a ticket, the request is same-site, the plane is not closing, the
// rate allows it, the body fits the cap and the schema; only then does the
// embedder run, under the panel's busy flag. Every refusal leaves a log line.
func (ps *panelSet) handleAction() http.HandlerFunc {
	crossOrigin := http.NewCrossOriginProtection()
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := ps.lookup(r.PathValue("id"))
		if !ok {
			writePanelError(w, http.StatusNotFound, "no such action")
			return
		}
		a, ok := findAction(p, r.PathValue("action"))
		if !ok {
			writePanelError(w, http.StatusNotFound, "no such action")
			return
		}
		// A ticket is one-time and travels in a URL; a state change must not
		// be reachable with one, however the route was guarded upstream.
		if auth.AdmittedViaTicket(r) {
			refuseAction(w, r, p, a, http.StatusUnauthorized, "ticket", "panel actions do not accept a ticket")
			return
		}
		// The outer router already refuses a cross-site write; repeated here
		// so this handler does not depend on where it is mounted.
		if err := crossOrigin.Check(r); err != nil {
			refuseAction(w, r, p, a, http.StatusForbidden, "cross-site", "cross-origin admin request refused")
			return
		}
		if ps.stopping() {
			refuseAction(w, r, p, a, http.StatusServiceUnavailable, "shutting-down", "plane is shutting down")
			return
		}
		if !ps.actionLimit.allow() {
			w.Header().Set("Retry-After", "1")
			refuseAction(w, r, p, a, http.StatusTooManyRequests, "rate-limited", "too many panel actions")
			return
		}
		body, status, reason, msg := readActionBody(w, r)
		if status != 0 {
			refuseAction(w, r, p, a, status, reason, msg)
			return
		}
		vals, err := a.Parse(body)
		if err != nil {
			refuseInvalid(w, r, p, a, err)
			return
		}
		ps.runAction(w, r, p, a, vals)
	}
}

// readActionBody reads the whole body under the cap. A declared length over
// the cap is refused before a byte is read.
func readActionBody(w http.ResponseWriter, r *http.Request) (body []byte, status int, reason, msg string) {
	if r.ContentLength > sep2admin.MaxActionBodyBytes {
		return nil, http.StatusRequestEntityTooLarge, "body-too-large", "request body too large"
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sep2admin.MaxActionBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, http.StatusRequestEntityTooLarge, "body-too-large", "request body too large"
		}
		return nil, http.StatusBadRequest, "body-unreadable", "request body unreadable"
	}
	return body, 0, "", ""
}

// refuseInvalid answers a refused submission with the declared field it is
// about, and logs the refusal.
func refuseInvalid(w http.ResponseWriter, r *http.Request, p sep2admin.Panel, a sep2admin.Action, err error) {
	var ve *sep2admin.ActionValueError
	body := struct {
		Error string `json:"error"`
		Field string `json:"field,omitempty"`
	}{Error: actionInvalidRefusal}
	if errors.As(err, &ve) {
		body.Field = ve.Field
	}
	log.Printf("admin: panel %q action %q: refused reason=invalid status=%d field=%q admission=%s remote=%s",
		p.ID, a.ID, http.StatusBadRequest, body.Field, auth.AdmissionPath(r), r.RemoteAddr)
	writePanelJSON(w, http.StatusBadRequest, body)
}

// runAction checks choices and runs a under the panel's busy flag, with a
// context that ends on the request or on plane shutdown. The embedder's
// refusals and the plane's own are told apart by what the closure captured,
// since InvokeView only carries a Descriptor and an error.
//
// A Run that ignores its context can outlive the request, so the attempt is
// logged before Run, the outcome after, and a Run that returns after the
// answer was sent logs a late outcome. The running count covers it for
// Plane.Close.
func (ps *panelSet) runAction(w http.ResponseWriter, r *http.Request, p sep2admin.Panel, a sep2admin.Action, vals sep2admin.ActionValues) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-ps.streamsDone:
			cancel()
		case <-ctx.Done():
		}
	}()

	var (
		result   sep2admin.ActionResult
		invalid  error
		refusal  *sep2admin.ActionRefusal
		answered atomic.Bool
	)
	_, err := ps.run(r.WithContext(ctx), p, func(ctx context.Context) (sep2admin.Descriptor, error) {
		if err := a.CheckChoices(ctx, vals); err != nil {
			var ve *sep2admin.ActionValueError
			if errors.As(err, &ve) {
				invalid = err
				return sep2admin.Descriptor{}, nil
			}
			return sep2admin.Descriptor{}, err
		}
		ps.running.n.Add(1)
		defer ps.running.n.Add(-1)
		auditAction(r, p, a, "started")
		res, err := a.Run(ctx, vals)
		var late string
		if answered.Load() {
			late = "late-"
		}
		switch {
		case errors.As(err, &refusal):
			auditAction(r, p, a, late+"refused")
			return sep2admin.Descriptor{}, nil
		case err != nil:
			auditAction(r, p, a, late+"failed")
		default:
			auditAction(r, p, a, late+"ok")
		}
		result = res
		return sep2admin.Descriptor{}, err
	})
	answered.Store(true)
	switch {
	case errors.Is(err, errPanelBusy):
		refuseAction(w, r, p, a, http.StatusGatewayTimeout, "busy", "panel is still answering an earlier request")
		return
	case errors.Is(err, sep2admin.ErrViewTimedOut), errors.Is(err, sep2admin.ErrViewNotInvoked):
		auditAction(r, p, a, "timeout")
		writePanelError(w, http.StatusGatewayTimeout, actionUnknownOutcome)
		return
	case errors.Is(err, sep2admin.ErrViewCanceled):
		auditAction(r, p, a, "canceled")
		writePanelError(w, http.StatusServiceUnavailable, actionCanceledOutcome)
		return
	case err != nil:
		ps.failure(p, err) // logs the detail
		writePanelError(w, http.StatusInternalServerError, actionFailedMessage)
		return
	}
	if invalid != nil {
		refuseInvalid(w, r, p, a, invalid)
		return
	}
	if refusal != nil {
		writePanelJSON(w, http.StatusUnprocessableEntity, struct {
			Error string `json:"error"`
		}{boundedActionText(refusal.Reason)})
		return
	}
	writePanelJSON(w, http.StatusOK, struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{true, boundedActionText(result.Message)})
}

// boundedActionText makes an embedder's sentence safe to send: valid UTF-8,
// cut to MaxActionMessageBytes at a rune boundary.
func boundedActionText(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	if len(s) <= sep2admin.MaxActionMessageBytes {
		return s
	}
	s = s[:sep2admin.MaxActionMessageBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
