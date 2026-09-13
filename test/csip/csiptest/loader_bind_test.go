package csiptest_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

const (
	boundLFDI            = "B0B0000000000000000000000000000000000001"
	boundSFDI            = "111111111111"
	otherBoundLFDI       = "B0B0000000000000000000000000000000000002"
	singleEdevFixtureLFD = "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"
	childLFDI1           = "C001000000000000000000000000000000000001"
	childLFDI2           = "C002000000000000000000000000000000000002"
	unmanagedLFDI        = "C003000000000000000000000000000000000003"
)

func singleEdevFixture() string { return filepath.Join("..", "fixtures", "single-edev.yaml") }

func TestLoad_BindOverwritesTheFixtureIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target := csiptest.NewTarget()
	id := csiptest.DeviceIdentity{LFDI: boundLFDI, SFDI: boundSFDI}

	if err := csiptest.Load(ctx, target, singleEdevFixture(), csiptest.Bind("0", id)); err != nil {
		t.Fatalf("Load with Bind: %v", err)
	}
	dev, err := target.EndDevices.Get(ctx, "0")
	if err != nil {
		t.Fatalf("Get bound EndDevice: %v", err)
	}
	if dev.LFDI != boundLFDI || dev.SFDI != boundSFDI {
		t.Errorf("bound EndDevice LFDI=%q SFDI=%q, want %q and %q", dev.LFDI, dev.SFDI, boundLFDI, boundSFDI)
	}
	if dev.Href != "/edev/0" {
		t.Errorf("binding changed the href to %q", dev.Href)
	}
	if byLFDI, err := target.EndDevices.GetByLFDI(ctx, boundLFDI); err != nil || byLFDI.Href != "/edev/0" {
		t.Errorf("GetByLFDI(bound) = %q, %v; want /edev/0", byLFDI.Href, err)
	}
	if _, err := target.EndDevices.GetByLFDI(ctx, singleEdevFixtureLFD); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the fixture's literal LFDI still resolves after binding: %v", err)
	}
}

func TestLoad_BindRefusesAnUnusableBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	id := csiptest.DeviceIdentity{LFDI: boundLFDI, SFDI: boundSFDI}
	other := csiptest.DeviceIdentity{LFDI: otherBoundLFDI, SFDI: "222222222222"}

	cases := map[string][]csiptest.LoadOption{
		"unknown EndDevice": {csiptest.Bind("9", id)},
		"bound twice":       {csiptest.Bind("0", id), csiptest.Bind("0", other)},
		"empty identity":    {csiptest.Bind("0", csiptest.DeviceIdentity{})},
	}
	for name, opts := range cases {
		target := csiptest.NewTarget()
		if err := csiptest.Load(ctx, target, singleEdevFixture(), opts...); err == nil {
			t.Errorf("%s: Load succeeded, want an error", name)
		}
		if n, _ := target.EndDevices.Count(ctx); n != 0 {
			t.Errorf("%s: %d EndDevice(s) written before the refusal", name, n)
		}
	}

	twoDevices := &csiptest.Spec{EndDevices: []csiptest.EndDeviceSpec{
		{ID: "0", SFDI: "100", LFDI: childLFDI1},
		{ID: "1", SFDI: "101", LFDI: childLFDI2},
	}}
	target := csiptest.NewTarget()
	if err := csiptest.LoadSpec(ctx, target, twoDevices, csiptest.Bind("0", id), csiptest.Bind("1", id)); err == nil {
		t.Error("one identity bound to two EndDevices: LoadSpec succeeded, want an error")
	}
}

func aggregatorSpec() *csiptest.Spec {
	return &csiptest.Spec{EndDevices: []csiptest.EndDeviceSpec{
		{ID: "0", SFDI: "100", LFDI: "A000000000000000000000000000000000000000"},
		{ID: "1", SFDI: "101", LFDI: childLFDI1, ManagedBy: "0"},
		{ID: "2", SFDI: "102", LFDI: childLFDI2, ManagedBy: "0"},
		{ID: "3", SFDI: "103", LFDI: unmanagedLFDI},
	}}
}

func TestLoadSpec_ManagedByRecordsPairsByBoundLFDI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target := csiptest.NewTarget()
	managers := memory.NewEndDeviceManagementStore()
	target.EndDeviceManagers = managers
	aggregator := csiptest.DeviceIdentity{LFDI: boundLFDI, SFDI: boundSFDI}

	if err := csiptest.LoadSpec(ctx, target, aggregatorSpec(), csiptest.Bind("0", aggregator)); err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if got, err := managers.ManagedBy(ctx, boundLFDI); err != nil || !slices.Equal(got, []string{childLFDI1, childLFDI2}) {
		t.Errorf("ManagedBy(aggregator) = %v, %v; want [%s %s]", got, err, childLFDI1, childLFDI2)
	}
	if got, err := managers.ManagerOf(ctx, childLFDI2); err != nil || got != boundLFDI {
		t.Errorf("ManagerOf(child 2) = %q, %v; want the bound aggregator", got, err)
	}
	if got, err := managers.ManagerOf(ctx, unmanagedLFDI); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ManagerOf(unmanaged) = %q, %v; want ErrNotFound", got, err)
	}
	if got, _ := managers.ManagedBy(ctx, "A000000000000000000000000000000000000000"); len(got) != 0 {
		t.Errorf("the pre-bind fixture LFDI manages %v; pairs must use the bound identity", got)
	}
}

func TestLoadSpec_ManagedByRefusesAnUnresolvablePair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := map[string]func(s *csiptest.Spec, target *csiptest.Target){
		"unknown manager id":  func(s *csiptest.Spec, _ *csiptest.Target) { s.EndDevices[1].ManagedBy = "7" },
		"manages itself":      func(s *csiptest.Spec, _ *csiptest.Target) { s.EndDevices[1].ManagedBy = "1" },
		"no management store": func(_ *csiptest.Spec, target *csiptest.Target) { target.EndDeviceManagers = nil },
		"manager has no LFDI": func(s *csiptest.Spec, _ *csiptest.Target) { s.EndDevices[0].LFDI = "" },
		"managed has no LFDI": func(s *csiptest.Spec, _ *csiptest.Target) { s.EndDevices[2].LFDI = "" },
	}
	for name, mutate := range cases {
		spec := aggregatorSpec()
		target := csiptest.NewTarget()
		managers := memory.NewEndDeviceManagementStore()
		target.EndDeviceManagers = managers
		mutate(spec, target)
		if err := csiptest.LoadSpec(ctx, target, spec); err == nil {
			t.Errorf("%s: LoadSpec succeeded, want an error", name)
		}
	}
}
