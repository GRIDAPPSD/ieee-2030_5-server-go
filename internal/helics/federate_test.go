//go:build helics

package helics_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/helics"
)

// TestFederate_PublishSubscribeLifecycle exercises the full happy-path
// loop: two federates join the same in-process federation, one publishes
// a value, the other subscribes, both step time forward, and the
// subscribed value arrives on the channel with the published payload.
//
// This is the IEEE-145 contract from phase-1 exit criterion 4: the test
// stands up the broker shape itself, so no external helics_broker
// process is required.
func TestFederate_PublishSubscribeLifecycle(t *testing.T) {
	root, err := helics.NewInProcess("ieee145-pubsub-root", "")
	if err != nil {
		t.Fatalf("NewInProcess root: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	rootAddr := root.Address()
	if rootAddr == "" {
		t.Fatalf("root.Address: expected non-empty")
	}

	// Publisher federate.
	pub, err := helics.NewFederate(context.Background(), helics.FederateConfig{
		Name:          "ieee145-pub",
		BrokerAddress: rootAddr,
		StepSize:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFederate(pub): unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	// Subscriber federate.
	sub, err := helics.NewFederate(context.Background(), helics.FederateConfig{
		Name:          "ieee145-sub",
		BrokerAddress: rootAddr,
		StepSize:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFederate(sub): unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	// Subscribe BEFORE entering executing mode — HELICS requires that
	// pubs/subs are registered while the federate is in initializing
	// state. The wrapper calls EnterExecutingMode on the first Step.
	ch, err := sub.Subscribe("ieee145-pub/active_power")
	if err != nil {
		t.Fatalf("Subscribe: unexpected error: %v", err)
	}

	// Register the publication on the publisher side.
	if err := pub.RegisterPublication("active_power", ""); err != nil {
		t.Fatalf("RegisterPublication: unexpected error: %v", err)
	}

	// Enter executing mode on both federates concurrently. HELICS
	// requires every federate in the federation to call this before
	// any of them can advance time, so a sequential call from a single
	// goroutine deadlocks.
	enterErr := make(chan error, 2)
	go func() { enterErr <- pub.EnterExecutingMode() }()
	go func() { enterErr <- sub.EnterExecutingMode() }()
	for i := 0; i < 2; i++ {
		if err := <-enterErr; err != nil {
			t.Fatalf("EnterExecutingMode (federate %d): %v", i, err)
		}
	}

	// Publish a recognizable value.
	const wantValue = 12345.6789
	if err := pub.Publish("active_power", wantValue); err != nil {
		t.Fatalf("Publish: unexpected error: %v", err)
	}

	// Drive both federates forward concurrently per timestep — Step
	// also blocks until all federates have requested a time, so we
	// fan out per iteration for the same reason as EnterExecutingMode.
	for i := 0; i < 5; i++ {
		stepErr := make(chan error, 2)
		go func() { _, err := pub.Step(100 * time.Millisecond); stepErr <- err }()
		go func() { _, err := sub.Step(100 * time.Millisecond); stepErr <- err }()
		for j := 0; j < 2; j++ {
			if err := <-stepErr; err != nil {
				t.Fatalf("Step (iter %d, fed %d): %v", i, j, err)
			}
		}
		select {
		case v := <-ch:
			// Per data-invariants rule 1: assert on the field value, not
			// just that something arrived. The HELICS double round-trip
			// is exact.
			if v.Value != wantValue {
				t.Fatalf("Subscribe channel: got Value=%v, want %v", v.Value, wantValue)
			}
			if v.Topic != "ieee145-pub/active_power" {
				t.Fatalf("Subscribe channel: got Topic=%q, want %q", v.Topic, "ieee145-pub/active_power")
			}
			return
		case <-time.After(500 * time.Millisecond):
			// Pump may not have caught up yet; try the next step.
		}
	}
	t.Fatalf("Subscribe: did not receive published value within 5 steps")
}

// TestFederate_BrokerAddressEmpty_OwnsInProcessBroker covers the AC
// clause that an empty FederateConfig.BrokerAddress spins up an
// in-process broker. The federate owns that broker for its lifetime
// and tears it down on Close.
func TestFederate_BrokerAddressEmpty_OwnsInProcessBroker(t *testing.T) {
	f, err := helics.NewFederate(context.Background(), helics.FederateConfig{
		Name:          "ieee145-self-broker",
		BrokerAddress: "",
		StepSize:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFederate (empty BrokerAddress): unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	if err := f.EnterExecutingMode(); err != nil {
		t.Fatalf("EnterExecutingMode: %v", err)
	}

	// Step forward once to prove the federation is live; HELICS would
	// surface a broker-side failure as a *HelicsError on RequestTime.
	if _, err := f.Step(100 * time.Millisecond); err != nil {
		t.Fatalf("Step: unexpected error: %v", err)
	}
}

// TestFederate_Close_Idempotent locks the idempotency contract: the
// first Close returns nil (or a HELICS-side disconnect error, which on
// a healthy federation is nil), the second returns ErrFederateClosed
// recoverable via errors.Is, and post-close inspectors are safe.
func TestFederate_Close_Idempotent(t *testing.T) {
	root, err := helics.NewInProcess("ieee145-close-root", "")
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	f, err := helics.NewFederate(context.Background(), helics.FederateConfig{
		Name:          "ieee145-close",
		BrokerAddress: root.Address(),
		StepSize:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFederate: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("Close (first call): unexpected error: %v", err)
	}

	err = f.Close()
	if !errors.Is(err, helics.ErrFederateClosed) {
		t.Fatalf("Close (second call): expected ErrFederateClosed, got %v", err)
	}
}

// TestFederate_ZeroValue_Safe proves that a zero-value *Federate cannot
// SIGSEGV across the cgo boundary: every method either returns
// ErrUninitializedFederate (recoverable via errors.Is) or a documented
// safe value.
func TestFederate_ZeroValue_Safe(t *testing.T) {
	var f helics.Federate

	err := f.Close()
	if !errors.Is(err, helics.ErrUninitializedFederate) {
		t.Fatalf("Close (zero-value): expected ErrUninitializedFederate, got %v", err)
	}
	// Idempotency contract: the second Close returns ErrFederateClosed,
	// same as on a properly-constructed federate.
	err = f.Close()
	if !errors.Is(err, helics.ErrFederateClosed) {
		t.Fatalf("Close (zero-value, second call): expected ErrFederateClosed, got %v", err)
	}

	// Methods on a fresh zero-value federate report uninitialized.
	var f2 helics.Federate
	if err := f2.RegisterPublication("topic", ""); !errors.Is(err, helics.ErrUninitializedFederate) {
		t.Fatalf("RegisterPublication (zero-value): expected ErrUninitializedFederate, got %v", err)
	}
	if _, err := f2.Subscribe("topic"); !errors.Is(err, helics.ErrUninitializedFederate) {
		t.Fatalf("Subscribe (zero-value): expected ErrUninitializedFederate, got %v", err)
	}
	if err := f2.Publish("topic", 1.0); !errors.Is(err, helics.ErrUninitializedFederate) {
		t.Fatalf("Publish (zero-value): expected ErrUninitializedFederate, got %v", err)
	}
	if err := f2.EnterExecutingMode(); !errors.Is(err, helics.ErrUninitializedFederate) {
		t.Fatalf("EnterExecutingMode (zero-value): expected ErrUninitializedFederate, got %v", err)
	}
	if _, err := f2.Step(time.Millisecond); !errors.Is(err, helics.ErrUninitializedFederate) {
		t.Fatalf("Step (zero-value): expected ErrUninitializedFederate, got %v", err)
	}
}

// TestFederate_RejectsEmptyTopic locks the wrapper-side validation that
// publish/subscribe topics must be non-empty: a dedicated sentinel
// (ErrEmptyTopic) keeps this lane distinct from HELICS error codes.
func TestFederate_RejectsEmptyTopic(t *testing.T) {
	root, err := helics.NewInProcess("ieee145-empty-topic-root", "")
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	f, err := helics.NewFederate(context.Background(), helics.FederateConfig{
		Name:          "ieee145-empty-topic",
		BrokerAddress: root.Address(),
		StepSize:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFederate: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	if err := f.RegisterPublication("", ""); !errors.Is(err, helics.ErrEmptyTopic) {
		t.Fatalf("RegisterPublication(\"\"): expected ErrEmptyTopic, got %v", err)
	}
	if _, err := f.Subscribe(""); !errors.Is(err, helics.ErrEmptyTopic) {
		t.Fatalf("Subscribe(\"\"): expected ErrEmptyTopic, got %v", err)
	}
	if err := f.Publish("", 1.0); !errors.Is(err, helics.ErrEmptyTopic) {
		t.Fatalf("Publish(\"\"): expected ErrEmptyTopic, got %v", err)
	}
}

// TestFederate_RejectsEmbeddedNUL covers the secure-coding rule 5 path:
// embedded NULs in any caller-supplied string would silently truncate
// inside C.CString. Mirrors the broker-side discipline.
func TestFederate_RejectsEmbeddedNUL(t *testing.T) {
	root, err := helics.NewInProcess("ieee145-nul-root", "")
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	t.Run("NUL in federate name", func(t *testing.T) {
		_, err := helics.NewFederate(context.Background(), helics.FederateConfig{
			Name:          "ieee145\x00pwn",
			BrokerAddress: root.Address(),
			StepSize:      100 * time.Millisecond,
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var herr *helics.HelicsError
		if !errors.As(err, &herr) {
			t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
		}
		if herr.Op != "NewFederate" {
			t.Fatalf("HelicsError.Op: expected NewFederate, got %q", herr.Op)
		}
		if !strings.Contains(herr.Message, "name") || !strings.Contains(herr.Message, "NUL") {
			t.Fatalf("HelicsError.Message: expected to mention name+NUL, got %q", herr.Message)
		}
	})

	t.Run("NUL in topic on Subscribe", func(t *testing.T) {
		f, err := helics.NewFederate(context.Background(), helics.FederateConfig{
			Name:          "ieee145-nul-topic",
			BrokerAddress: root.Address(),
			StepSize:      100 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewFederate: %v", err)
		}
		t.Cleanup(func() { _ = f.Close() })
		_, err = f.Subscribe("topic\x00pwn")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var herr *helics.HelicsError
		if !errors.As(err, &herr) {
			t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
		}
		if !strings.Contains(herr.Message, "topic") || !strings.Contains(herr.Message, "NUL") {
			t.Fatalf("HelicsError.Message: expected to mention topic+NUL, got %q", herr.Message)
		}
	})
}

// TestFederate_RejectsMalformedBrokerAddress covers the AC's CLI-style
// boundary: the federate's HELICS init string is constructed from the
// caller-supplied BrokerAddress, so whitespace and leading dashes have
// the same re-tokenization risk the broker's NewSubBroker guards
// against. Reuses the rejectNUL/whitespace/leading-dash discipline.
func TestFederate_RejectsMalformedBrokerAddress(t *testing.T) {
	cases := []struct {
		label    string
		address  string
		wantSnip string
	}{
		{"NUL", "tcp://1.2.3.4\x00:23404", "NUL"},
		{"space", "tcp://1.2.3.4 :23404", "whitespace"},
		{"tab", "tcp://1.2.3.4\t:23404", "whitespace"},
		{"newline", "tcp://1.2.3.4\n:23404", "whitespace"},
		{"carriage return", "tcp://1.2.3.4\r:23404", "whitespace"},
		{"leading dash", "-malicious-flag", "leading dash"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := helics.NewFederate(context.Background(), helics.FederateConfig{
				Name:          "ieee145-bad-addr",
				BrokerAddress: tc.address,
				StepSize:      100 * time.Millisecond,
			})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var herr *helics.HelicsError
			if !errors.As(err, &herr) {
				t.Fatalf("expected *helics.HelicsError, got %T: %v", err, err)
			}
			if herr.Op != "NewFederate" {
				t.Fatalf("HelicsError.Op: expected NewFederate, got %q", herr.Op)
			}
			if !strings.Contains(herr.Message, tc.wantSnip) {
				t.Fatalf("HelicsError.Message: expected %q, got %q", tc.wantSnip, herr.Message)
			}
		})
	}
}

// TestFederate_Subscribe_ChannelClosesOnFederateClose proves the
// channel-lifetime contract: the goroutine pumping values into the
// Subscribe channel exits when the federate closes, so consumers can
// detect shutdown via a closed channel without leaking goroutines.
func TestFederate_Subscribe_ChannelClosesOnFederateClose(t *testing.T) {
	root, err := helics.NewInProcess("ieee145-chan-close-root", "")
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	f, err := helics.NewFederate(context.Background(), helics.FederateConfig{
		Name:          "ieee145-chan-close",
		BrokerAddress: root.Address(),
		StepSize:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFederate: %v", err)
	}

	ch, err := f.Subscribe("anything/active_power")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Consumer reads until close. A buffered channel may have residual
	// values; what we assert is that the channel eventually closes.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed — contract satisfied
			}
		case <-deadline:
			t.Fatalf("Subscribe channel did not close within 2s of Federate.Close")
		}
	}
}
