package sep2adminplane

import (
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// Settings holds the four admin-plane values the standalone server reads
// from its environment, parsed by the same functions it uses.
//
// Settings alone does not make a 2023 server. The caller also sets
// assembly.RouterConfig and Stores.Edition2023 from Edition, because New
// refuses an Edition that disagrees with Stores.Edition2023 and the protocol
// router serves what the stores say.
//
// A zero field means unset: New resolves it to the server's default (edition
// "2018", deadline 300 s, grace 1800 s).
type Settings struct {
	// Edition is "", "2018" or "2023". Env: SEP2_EDITION.
	Edition string
	// PEN is the Private Enterprise Number; nil when unset. Env: SEP2_PEN.
	PEN *uint32
	// FlowReservationDeadline is from 1s to 1h, or zero. Env:
	// SEP2_FLOW_RESERVATION_DEADLINE_SECONDS.
	FlowReservationDeadline time.Duration
	// RetentionGrace is whole seconds from 15m to 168h, or zero. Env:
	// SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS.
	RetentionGrace time.Duration
}

// SettingsFromEnv parses the four settings through getenv. An invalid value
// returns an error that names its variable.
func SettingsFromEnv(getenv func(string) string) (Settings, error) {
	var s Settings
	var err error
	if s.PEN, err = config.ParsePEN(getenv("SEP2_PEN")); err != nil {
		return Settings{}, err
	}
	if s.Edition, err = config.ParseSEP2Edition(getenv("SEP2_EDITION")); err != nil {
		return Settings{}, err
	}
	if s.FlowReservationDeadline, err = config.ParseFlowReservationDeadlineSeconds(getenv("SEP2_FLOW_RESERVATION_DEADLINE_SECONDS")); err != nil {
		return Settings{}, err
	}
	if s.RetentionGrace, err = config.ParseFlowReservationRetentionGraceSeconds(getenv("SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS")); err != nil {
		return Settings{}, err
	}
	return s, nil
}
