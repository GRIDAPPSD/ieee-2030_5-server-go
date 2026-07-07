package auth_test

import (
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

func TestTicketIssueAndRedeem(t *testing.T) {
	store := auth.NewTicketStore(30 * time.Second)

	ticket, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket")
	}

	if !store.Redeem(ticket) {
		t.Error("valid ticket should be redeemable")
	}
}

func TestTicketOneTimeUse(t *testing.T) {
	store := auth.NewTicketStore(30 * time.Second)

	ticket, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}

	if !store.Redeem(ticket) {
		t.Fatal("first redeem should succeed")
	}
	if store.Redeem(ticket) {
		t.Error("second redeem should fail (one-time use)")
	}
}

func TestTicketExpired(t *testing.T) {
	store := auth.NewTicketStore(1 * time.Millisecond)

	ticket, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)

	if store.Redeem(ticket) {
		t.Error("expired ticket should not be redeemable")
	}
}

func TestTicketEmptyString(t *testing.T) {
	store := auth.NewTicketStore(30 * time.Second)

	if store.Redeem("") {
		t.Error("empty ticket should not be redeemable")
	}
}

func TestTicketUnknown(t *testing.T) {
	store := auth.NewTicketStore(30 * time.Second)

	if store.Redeem("nonexistent-ticket-value") {
		t.Error("unknown ticket should not be redeemable")
	}
}

func TestTicketPurgeOnIssue(t *testing.T) {
	store := auth.NewTicketStore(1 * time.Millisecond)

	_, err := store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 1 {
		t.Fatalf("expected 1 ticket, got %d", store.Len())
	}

	time.Sleep(5 * time.Millisecond)

	// Issue a new ticket; the expired one should be purged.
	_, err = store.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 1 {
		t.Errorf("expected 1 ticket after purge, got %d", store.Len())
	}
}

func TestTicketUniqueness(t *testing.T) {
	store := auth.NewTicketStore(30 * time.Second)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		ticket, err := store.Issue()
		if err != nil {
			t.Fatal(err)
		}
		if seen[ticket] {
			t.Fatalf("duplicate ticket on iteration %d", i)
		}
		seen[ticket] = true
	}
}
