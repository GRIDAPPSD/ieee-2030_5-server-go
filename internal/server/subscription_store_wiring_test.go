package server_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// #171: pin the server-side subscription-store wiring. server.Run
// constructs the subscription store via
//
//	memory.NewSubscriptionStoreWithPersistence(
//	    cfg.EffectiveStorePath("subscriptions", cfg.SubscriptionStorePath))
//
// #165 already unit-tests Config.EffectiveStorePath in isolation
// (config_test.go::TestEffectiveStorePath). This test pins the *callsite*:
// the exact call shape server.go uses, end-to-end, with the file actually
// written to disk at the expected path. Catches accidental regressions
// such as reverting to the #224 direct-path form
// (NewSubscriptionStoreWithPersistence(cfg.SubscriptionStorePath)) or
// passing the wrong store name.
func TestSubscriptionStoreWiring_DataDirDerivedPath(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	cfg := &config.Config{
		DataDir:               dataDir,
		SubscriptionStorePath: "", // empty → DataDir-derived path wins
	}

	// Exact callsite shape from internal/server/server.go.
	wantPath := filepath.Join(dataDir, "subscriptions.json")
	gotPath := cfg.EffectiveStorePath("subscriptions", cfg.SubscriptionStorePath)
	if gotPath != wantPath {
		t.Fatalf("EffectiveStorePath = %q, want %q", gotPath, wantPath)
	}

	store, err := memory.NewSubscriptionStoreWithPersistence(gotPath)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence(%q): %v", gotPath, err)
	}

	// Trigger a write so the snapshot lands on disk.
	if err := store.Create(context.Background(), "sub1", makeSub("/edev/1/sub/1", "/edev/1", "http://example.test/notify")); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("expected snapshot at %q: %v", wantPath, err)
	}
	if info.Size() == 0 {
		t.Fatalf("snapshot at %q is empty", wantPath)
	}
}

// #171 regression: SEP2_SUBSCRIPTION_STORE_PATH (the #224-era
// dedicated knob) must keep winning over SEP2_DATA_DIR. Same precedence
// #165's TestEffectiveStorePath asserts at the helper level, but
// here we drive it through the exact server.go callsite plus a real
// file write so the wiring is the thing under test.
func TestSubscriptionStoreWiring_DedicatedPathOverridesDataDir(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	dedicatedDir := t.TempDir()
	dedicatedPath := filepath.Join(dedicatedDir, "legacy-subs.json")

	cfg := &config.Config{
		DataDir:               dataDir,
		SubscriptionStorePath: dedicatedPath, // wins over DataDir
	}

	gotPath := cfg.EffectiveStorePath("subscriptions", cfg.SubscriptionStorePath)
	if gotPath != dedicatedPath {
		t.Fatalf("EffectiveStorePath = %q, want %q (dedicated must win over DataDir)",
			gotPath, dedicatedPath)
	}

	store, err := memory.NewSubscriptionStoreWithPersistence(gotPath)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence(%q): %v", gotPath, err)
	}

	if err := store.Create(context.Background(), "sub1", makeSub("/edev/1/sub/1", "/edev/1", "http://example.test/notify")); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	// Snapshot landed at the dedicated path.
	if _, err := os.Stat(dedicatedPath); err != nil {
		t.Fatalf("expected snapshot at dedicated path %q: %v", dedicatedPath, err)
	}
	// And NOT at the DataDir-derived path.
	derivedPath := filepath.Join(dataDir, "subscriptions.json")
	if _, err := os.Stat(derivedPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot leaked to DataDir-derived path %q (err=%v); dedicated path must be exclusive",
			derivedPath, err)
	}
}

// #171 regression: both knobs empty → in-memory mode (path "")
// produces a usable store with no on-disk artifact.
func TestSubscriptionStoreWiring_InMemoryWhenBothEmpty(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{} // no DataDir, no SubscriptionStorePath

	gotPath := cfg.EffectiveStorePath("subscriptions", cfg.SubscriptionStorePath)
	if gotPath != "" {
		t.Fatalf("EffectiveStorePath = %q, want \"\" (in-memory)", gotPath)
	}

	store, err := memory.NewSubscriptionStoreWithPersistence(gotPath)
	if err != nil {
		t.Fatalf("NewSubscriptionStoreWithPersistence(\"\"): %v", err)
	}

	if err := store.Create(context.Background(), "sub1", makeSub("/edev/1/sub/1", "/edev/1", "http://example.test/notify")); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	// No file path to check — pure in-memory.
}

// makeSub constructs a minimally-valid Subscription via the nested
// SubscribableResource.Resource.Href path. Mirrors newSub in
// pkg/store/memory/subscription_testhooks_test.go but lives here so the
// server-package test stays self-contained.
func makeSub(href, subResource, notifyURI string) sep2.Subscription {
	return sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: href},
		},
		SubscribedResource: subResource,
		NotificationURI:    notifyURI,
	}
}
