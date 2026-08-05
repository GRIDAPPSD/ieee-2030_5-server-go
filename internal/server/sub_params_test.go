// package server (white-box): tests for resolveSubParam, the helper that
// reads SEP2_SUBSCRIPTION_WORKERS and SEP2_SUBSCRIPTION_QUEUE_SIZE from
// the environment and falls back to the compile-time defaults on bad input.
package server

import (
	"testing"
)

// resolveSubParam tests use t.Setenv, which is incompatible with t.Parallel
// per the Go testing contract (Setenv acquires the environment lock and
// cannot share it with parallel subtests).

func TestResolveSubParam_DefaultWhenUnset(t *testing.T) {
	// Env var absent: must return the supplied default unchanged.
	// t.Setenv restores the original value after the test completes.
	t.Setenv("SEP2_SUBSCRIPTION_WORKERS", "")
	got := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", 4)
	if got != 4 {
		t.Errorf("resolveSubParam (unset) = %d, want 4", got)
	}
}

func TestResolveSubParam_ValidOverride(t *testing.T) {
	t.Setenv("SEP2_SUBSCRIPTION_WORKERS", "8")
	got := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", 4)
	if got != 8 {
		t.Errorf("resolveSubParam (valid override) = %d, want 8", got)
	}
}

func TestResolveSubParam_QueueSizeValidOverride(t *testing.T) {
	t.Setenv("SEP2_SUBSCRIPTION_QUEUE_SIZE", "512")
	got := resolveSubParam("SEP2_SUBSCRIPTION_QUEUE_SIZE", 256)
	if got != 512 {
		t.Errorf("resolveSubParam (valid queue override) = %d, want 512", got)
	}
}

func TestResolveSubParam_FallbackOnNonInteger(t *testing.T) {
	// Non-integer value must fall back to the default; no panic.
	t.Setenv("SEP2_SUBSCRIPTION_WORKERS", "notanumber")
	got := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", 4)
	if got != 4 {
		t.Errorf("resolveSubParam (non-integer) = %d, want 4 (default)", got)
	}
}

func TestResolveSubParam_FallbackOnZero(t *testing.T) {
	// Zero is not a valid positive integer; must fall back to the default.
	t.Setenv("SEP2_SUBSCRIPTION_WORKERS", "0")
	got := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", 4)
	if got != 4 {
		t.Errorf("resolveSubParam (zero) = %d, want 4 (default)", got)
	}
}

func TestResolveSubParam_FallbackOnNegative(t *testing.T) {
	// Negative is not a valid positive integer; must fall back to the default.
	t.Setenv("SEP2_SUBSCRIPTION_QUEUE_SIZE", "-1")
	got := resolveSubParam("SEP2_SUBSCRIPTION_QUEUE_SIZE", 256)
	if got != 256 {
		t.Errorf("resolveSubParam (negative) = %d, want 256 (default)", got)
	}
}

func TestResolveSubParam_WhitespacePaddedValue(t *testing.T) {
	// A value with leading/trailing whitespace (easy to produce from a shell
	// export or a sweep script) must resolve to the integer, not fall back to
	// the default. Covers a Pike LOW finding.
	t.Setenv("SEP2_SUBSCRIPTION_WORKERS", " 8 ")
	got := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", 4)
	if got != 8 {
		t.Errorf("resolveSubParam (whitespace-padded) = %d, want 8", got)
	}
}

func TestResolveSubParam_DefaultsMatchHardcodedConstants(t *testing.T) {
	// When both env vars are unset the constants stay at the original values,
	// confirming the default behavior is byte-for-byte unchanged.
	t.Setenv("SEP2_SUBSCRIPTION_WORKERS", "")
	t.Setenv("SEP2_SUBSCRIPTION_QUEUE_SIZE", "")
	workers := resolveSubParam("SEP2_SUBSCRIPTION_WORKERS", subscriptionWorkers)
	queue := resolveSubParam("SEP2_SUBSCRIPTION_QUEUE_SIZE", subscriptionQueueSize)
	if workers != subscriptionWorkers {
		t.Errorf("workers default = %d, want constant %d", workers, subscriptionWorkers)
	}
	if queue != subscriptionQueueSize {
		t.Errorf("queue default = %d, want constant %d", queue, subscriptionQueueSize)
	}
}
