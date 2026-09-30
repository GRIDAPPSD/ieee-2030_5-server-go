package flowreservation

import "time"

// DefaultDeadline is #666's "default 300 s" bound on how long a request
// waits for an operator answer before the fallback decides.
const DefaultDeadline = 300 * time.Second

// DefaultRetryBackoff is the pause between deadline-fallback retry
// attempts after an infrastructure failure (a failed Get, mint or Create),
// not a decision refusal.
const DefaultRetryBackoff = 5 * time.Second

// DefaultRetryAttempts bounds how many times the deadline fallback retries
// an infrastructure failure before giving up and leaving the request
// unanswered, logged.
const DefaultRetryAttempts = 5

// Config bounds the deadline fallback. The zero value takes the package
// defaults.
type Config struct {
	// Deadline is the configured bound in issue #666's D1/D2: this many
	// seconds after creation, or the requested start if that comes sooner,
	// Queue answers automatically when the operator has not. Zero takes
	// DefaultDeadline.
	Deadline time.Duration

	// RetryBackoff is the pause between retries after the fallback's Get,
	// mint or Create fails for a reason other than the request having
	// disappeared. Zero takes DefaultRetryBackoff.
	RetryBackoff time.Duration

	// RetryAttempts bounds the number of fallback attempts (the first try
	// plus this many retries) before Queue gives up and logs. Zero takes
	// DefaultRetryAttempts.
	RetryAttempts int
}

func (c Config) withDefaults() Config {
	if c.Deadline <= 0 {
		c.Deadline = DefaultDeadline
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = DefaultRetryBackoff
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = DefaultRetryAttempts
	}
	return c
}
