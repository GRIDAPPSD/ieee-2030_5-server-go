package sep2adminplane

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Errors New returns for a Config it refuses; test for them with errors.Is.
var (
	// ErrNoCredential: AdminKey is blank (empty or whitespace only). With no
	// key and the bypass off, nothing could reach the plane.
	ErrNoCredential = errors.New("sep2adminplane: admin key is blank")
	// ErrNoAllowedHosts: AllowedHosts names no host. An empty list turns the
	// Host-header gate off, which leaves the plane open to DNS rebinding.
	ErrNoAllowedHosts = errors.New("sep2adminplane: no allowed hosts")
	// ErrNoStores: Stores is nil.
	ErrNoStores = errors.New("sep2adminplane: no stores")
	// ErrShortCredential: AdminKey is shorter than MinAdminKeyLength.
	ErrShortCredential = errors.New("sep2adminplane: admin key is too short")
	// ErrBlankAllowedHost: an AllowedHosts entry is blank.
	ErrBlankAllowedHost = errors.New("sep2adminplane: blank allowed host")
	// ErrUnknownEdition: Edition is not "", "2018" or "2023".
	ErrUnknownEdition = errors.New("sep2adminplane: unknown edition")
	// ErrEditionMismatch: Edition disagrees with Stores.Edition2023, which
	// the protocol router serves.
	ErrEditionMismatch = errors.New("sep2adminplane: edition disagrees with the stores")
	// ErrReadOnlyWithControlWrites: ReadOnly and ControlWrites are both set,
	// and they ask for opposite things.
	ErrReadOnlyWithControlWrites = errors.New("sep2adminplane: ReadOnly and ControlWrites are both set")
)

// MinAdminKeyLength is the shortest AdminKey New accepts, in characters.
// With the loopback bypass off the key is the only barrier to the plane.
const MinAdminKeyLength = 16

// Config is what New builds the plane from.
type Config struct {
	// Stores is the same set the protocol router serves, so an admin write
	// is what a device reads.
	Stores *assembly.Stores
	// AdminKey is the Bearer credential, and the password the login form
	// takes. It must be at least MinAdminKeyLength characters.
	AdminKey string
	// AllowedHosts are the Host header values the plane answers; any other
	// gets 421. It must name at least one host and hold no blank entry.
	AllowedHosts []string
	// Edition is the IEEE 2030.5 edition, "2018" or "2023"; empty means
	// "2018". It must agree with Stores.Edition2023.
	Edition string
	// PEN is the IANA Private Enterprise Number in the low 32 bits of every
	// DER control mRID the plane issues. Nil or 0 leaves DER control create
	// answering 503. New copies the value.
	PEN *uint32
	// FlowReservationDeadline is the hold the admin API reports each
	// request's deadline under; it must match the queue's. Zero means the
	// server's default, and a set value must be from 1s to 1h.
	FlowReservationDeadline time.Duration
	// RetentionGrace is how long an ended flow reservation stays readable.
	// Zero means the server's default, and a set value must be whole
	// seconds from 15m to 168h.
	RetentionGrace time.Duration
	// Notifier fans out the subscription notifications an admin write
	// causes. Nil sends none.
	Notifier assembly.ResourceNotifier
	// LoopbackBypass admits a loopback request that carries no credential
	// and no forwarded header, as the standalone server does. Leave it off
	// unless nothing else on the host can reach the listener.
	LoopbackBypass bool
	// ControlWrites mounts five routes: DER control create and cancel, and
	// flow reservation answer, revise and cancel. Off, only those five are
	// unmounted. Every other admin write stays, FSA program attach and
	// device FSA assignment among them, which also change what a device is
	// told.
	ControlWrites bool
	// ReadOnly mounts no write route except POST /auth/login and POST
	// /auth/ticket, so an embedder that seeds the stores itself has no second
	// writer. A write then gets 404 or 405; every GET route stays. It cannot
	// be combined with ControlWrites.
	ReadOnly bool
	// Panels are extra tabs the shell shows after its own. A panel the
	// registry refuses makes New fail.
	Panels []sep2admin.Panel
	// PanelActions serves the typed actions the panels declare, under
	// /api/ui/panels/{id}/actions. Off, those routes answer 404. It works
	// with ReadOnly: an action changes what its embedder does, not the
	// stores. Call Plane.Close before shutting the listener so a running
	// action is told to stop and is waited for.
	PanelActions bool
}

// Plane is a built admin plane.
type Plane struct {
	handler     http.Handler
	patterns    []string
	streamsDone chan struct{}
	closeOnce   sync.Once
	actions     *adminplane.ActionTracker
}

// New builds the plane. It refuses a blank or short AdminKey, an empty
// AllowedHosts or one with a blank entry, a nil Stores, an unknown or
// disagreeing Edition, ReadOnly with ControlWrites, an out-of-range deadline or grace, and a panel the
// registry refuses.
func New(cfg Config) (*Plane, error) {
	if auth.IsBlankCredential(cfg.AdminKey) {
		return nil, ErrNoCredential
	}
	if utf8.RuneCountInString(cfg.AdminKey) < MinAdminKeyLength {
		return nil, ErrShortCredential
	}
	if len(cfg.AllowedHosts) == 0 {
		return nil, ErrNoAllowedHosts
	}
	if slices.ContainsFunc(cfg.AllowedHosts, func(h string) bool { return strings.TrimSpace(h) == "" }) {
		return nil, ErrBlankAllowedHost
	}
	if cfg.Stores == nil {
		return nil, ErrNoStores
	}
	if cfg.ReadOnly && cfg.ControlWrites {
		return nil, ErrReadOnlyWithControlWrites
	}
	edition, deadline, grace, err := resolveSettings(cfg)
	if err != nil {
		return nil, err
	}

	stores, err := adminStores(cfg, edition, deadline, grace)
	if err != nil {
		return nil, err
	}

	streamsDone := make(chan struct{})
	actions := &adminplane.ActionTracker{}
	h, patterns, err := adminplane.Build(adminplane.Config{
		StreamsDone:    streamsDone,
		AdminKey:       cfg.AdminKey,
		Stores:         stores,
		Tickets:        auth.NewTicketStore(adminplane.AdminTicketTTL),
		Sessions:       auth.NewSessionStore(adminplane.AdminSessionIdleTimeout, adminplane.AdminSessionAbsoluteTimeout),
		AllowedHosts:   slices.Clone(cfg.AllowedHosts),
		Panels:         cfg.Panels,
		LoopbackBypass: cfg.LoopbackBypass,
		ControlWrites:  cfg.ControlWrites,
		ReadOnly:       cfg.ReadOnly,
		PanelActions:   cfg.PanelActions,
		ActionTracker:  actions,
	})
	if err != nil {
		return nil, fmt.Errorf("sep2adminplane: %w", err)
	}
	return &Plane{handler: h, patterns: patterns, streamsDone: streamsDone, actions: actions}, nil
}

// resolveSettings turns the edition, deadline and grace of cfg into the values
// the plane runs under: an unset one takes the server's default.
func resolveSettings(cfg Config) (handler.SEP2Edition, time.Duration, time.Duration, error) {
	edition, err := resolveEdition(cfg.Edition, cfg.Stores.Edition2023)
	if err != nil {
		return "", 0, 0, err
	}
	durations := config.Config{FlowReservationDeadline: cfg.FlowReservationDeadline, FlowReservationRetentionGrace: cfg.RetentionGrace}
	deadline, err := durations.EffectiveFlowReservationDeadline()
	if err != nil {
		return "", 0, 0, fmt.Errorf("sep2adminplane: %w", err)
	}
	grace, err := durations.EffectiveFlowReservationRetentionGrace()
	if err != nil {
		return "", 0, 0, fmt.Errorf("sep2adminplane: %w", err)
	}
	return edition, deadline, grace, nil
}

// adminStores is the admin plane's view of cfg.Stores with the settings New
// resolved.
func adminStores(cfg Config, edition handler.SEP2Edition, deadline, grace time.Duration) (*adminplane.Stores, error) {
	stores, err := adminplane.StoresFromAssembly(cfg.Stores)
	if err != nil {
		return nil, fmt.Errorf("sep2adminplane: %w", err)
	}
	stores.Sep2Edition = edition
	// A copy, so the caller cannot change the PEN after the zero check.
	if cfg.PEN != nil && *cfg.PEN != 0 {
		pen := *cfg.PEN
		stores.PEN = &pen
	}
	stores.FlowReservationDeadline = deadline
	stores.FlowReservationRetentionGrace = grace
	// AdaptNotifier is nil for a nil or typed-nil notifier, which would
	// otherwise panic on the first admin write.
	if adminplane.AdaptNotifier(cfg.Notifier) != nil {
		stores.AdminNotifier = cfg.Notifier
	}
	return stores, nil
}

// resolveEdition maps Config.Edition onto the handler's edition and refuses
// one that disagrees with the stores: the protocol router serves
// Stores.Edition2023, and the admin views must read the same rule.
func resolveEdition(edition string, stores2023 bool) (handler.SEP2Edition, error) {
	var e handler.SEP2Edition
	switch edition {
	case "", string(handler.Edition2018):
		e = handler.Edition2018
	case string(handler.Edition2023):
		e = handler.Edition2023
	default:
		return "", fmt.Errorf("%w: %q is not 2018 or 2023", ErrUnknownEdition, edition)
	}
	if (e == handler.Edition2023) != stores2023 {
		return "", fmt.Errorf("%w: edition %s, Stores.Edition2023=%t", ErrEditionMismatch, e, stores2023)
	}
	return e, nil
}

// CloseStreams ends every open panel stream with a final status event and
// refuses new ones. Call it before the serving http.Server's Shutdown, or
// register it with RegisterOnShutdown: Shutdown does not cancel request
// contexts, so an open stream would otherwise hold it to its deadline.
// Calling it more than once is safe.
func (p *Plane) CloseStreams() { p.closeOnce.Do(func() { close(p.streamsDone) }) }

// Handler serves the admin UI at /ui/ and the admin API under /api/.
func (p *Plane) Handler() http.Handler { return p.handler }

// Close does what CloseStreams does, which also tells every running panel
// action to stop and makes the plane refuse new ones with 503, then waits up
// to wait for the running actions to return. It returns how many were still
// running at the bound, 0 when all had returned. A Run that ignores its
// context is not stopped, only counted, so a nonzero result means an action
// may still take effect after Close returns. It does not stop the listener,
// which the caller owns, and is safe to call more than once.
func (p *Plane) Close(wait time.Duration) int {
	p.CloseStreams()
	deadline := time.Now().Add(wait)
	for {
		n := p.actions.Running()
		if n == 0 || !time.Now().Before(deadline) {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Patterns lists every route mounted under the plane, sorted. The slice is
// the caller's.
func (p *Plane) Patterns() []string { return slices.Clone(p.patterns) }
