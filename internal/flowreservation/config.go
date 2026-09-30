package flowreservation

import "time"

// DefaultDeadline is #666's "default 300 s" bound on how long a request
// waits for an operator answer before the fallback decides.
const DefaultDeadline = 300 * time.Second

// Config bounds the deadline fallback. The zero value takes DefaultDeadline.
type Config struct {
	// Deadline is the configured bound in issue #666's D1/D2: this many
	// seconds after creation, or the requested start if that comes sooner,
	// Queue answers automatically when the operator has not. Zero takes
	// DefaultDeadline.
	Deadline time.Duration
}

func (c Config) withDefaults() Config {
	if c.Deadline <= 0 {
		c.Deadline = DefaultDeadline
	}
	return c
}
