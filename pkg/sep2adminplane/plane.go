package sep2adminplane

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

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
)

// Config is what New builds the plane from.
type Config struct {
	// Stores is the same set the protocol router serves, so an admin write
	// is what a device reads.
	Stores *assembly.Stores
	// AdminKey is the Bearer credential, and the password the login form
	// takes. It must not be blank.
	AdminKey string
	// AllowedHosts are the Host header values the plane answers; any other
	// gets 421. It must name at least one host.
	AllowedHosts []string
	// Edition is the IEEE 2030.5 edition, "2018" or "2023"; empty means
	// "2018". It must agree with Stores.Edition2023.
	Edition string
	// PEN is the IANA Private Enterprise Number in the low 32 bits of every
	// DER control mRID the plane issues. Nil or 0 leaves DER control create
	// answering 503.
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
	// ControlWrites mounts the DER control create and cancel routes and the
	// flow reservation answer, revise and cancel routes. Off, their reads
	// stay mounted.
	ControlWrites bool
	// Panels are extra tabs the shell shows after its own. A panel the
	// registry refuses makes New fail.
	Panels []sep2admin.Panel
}

// Plane is a built admin plane.
type Plane struct {
	handler  http.Handler
	patterns []string
}

// New builds the plane. It refuses a blank AdminKey, an AllowedHosts list
// naming no host, a nil Stores, an unknown or disagreeing Edition, an
// out-of-range deadline or grace, and a panel the registry refuses.
func New(cfg Config) (*Plane, error) {
	if auth.IsBlankCredential(cfg.AdminKey) {
		return nil, ErrNoCredential
	}
	if !slices.ContainsFunc(cfg.AllowedHosts, func(h string) bool { return strings.TrimSpace(h) != "" }) {
		return nil, ErrNoAllowedHosts
	}
	if cfg.Stores == nil {
		return nil, ErrNoStores
	}
	edition, err := resolveEdition(cfg.Edition, cfg.Stores.Edition2023)
	if err != nil {
		return nil, err
	}
	durations := config.Config{FlowReservationDeadline: cfg.FlowReservationDeadline, FlowReservationRetentionGrace: cfg.RetentionGrace}
	deadline, err := durations.EffectiveFlowReservationDeadline()
	if err != nil {
		return nil, fmt.Errorf("sep2adminplane: %w", err)
	}
	grace, err := durations.EffectiveFlowReservationRetentionGrace()
	if err != nil {
		return nil, fmt.Errorf("sep2adminplane: %w", err)
	}

	stores, err := adminplane.StoresFromAssembly(cfg.Stores)
	if err != nil {
		return nil, fmt.Errorf("sep2adminplane: %w", err)
	}
	stores.Sep2Edition = edition
	stores.PEN = cfg.PEN
	if cfg.PEN != nil && *cfg.PEN == 0 {
		stores.PEN = nil
	}
	stores.FlowReservationDeadline = deadline
	stores.FlowReservationRetentionGrace = grace
	// AdaptNotifier is nil for a nil or typed-nil notifier, which would
	// otherwise panic on the first admin write.
	if adminplane.AdaptNotifier(cfg.Notifier) != nil {
		stores.AdminNotifier = cfg.Notifier
	}

	h, patterns, err := adminplane.Build(adminplane.Config{
		AdminKey:       cfg.AdminKey,
		Stores:         stores,
		Tickets:        auth.NewTicketStore(adminplane.AdminTicketTTL),
		Sessions:       auth.NewSessionStore(adminplane.AdminSessionIdleTimeout, adminplane.AdminSessionAbsoluteTimeout),
		AllowedHosts:   slices.Clone(cfg.AllowedHosts),
		Panels:         cfg.Panels,
		LoopbackBypass: cfg.LoopbackBypass,
		ControlWrites:  cfg.ControlWrites,
	})
	if err != nil {
		return nil, fmt.Errorf("sep2adminplane: %w", err)
	}
	return &Plane{handler: h, patterns: patterns}, nil
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
		return "", fmt.Errorf("sep2adminplane: edition %q is not 2018 or 2023", edition)
	}
	if (e == handler.Edition2023) != stores2023 {
		return "", fmt.Errorf("sep2adminplane: edition %s disagrees with Stores.Edition2023=%t", e, stores2023)
	}
	return e, nil
}

// Handler serves the admin UI at /ui/ and the admin API under /api/.
func (p *Plane) Handler() http.Handler { return p.handler }

// Patterns lists every route mounted under the plane, sorted. The slice is
// the caller's.
func (p *Plane) Patterns() []string { return slices.Clone(p.patterns) }
