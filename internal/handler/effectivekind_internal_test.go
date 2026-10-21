package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const kgInternalLFDI = "D100000000000000000000000000000000000001"

type internalErrProfiles struct{ store.DeviceProfileStore }

func (internalErrProfiles) Get(context.Context, string) (store.DeviceProfile, error) {
	return store.DeviceProfile{}, errors.New("profile backend down")
}

type internalErrPairs struct {
	store.EndDeviceManagementReader
}

func (internalErrPairs) ManagerOf(context.Context, string) (string, error) {
	return "", errors.New("pair backend down")
}

func (internalErrPairs) ManagedBy(context.Context, string) ([]string, error) {
	return nil, errors.New("pair backend down")
}

func TestKindGateLookupErrorsNeverAllow(t *testing.T) {
	ctx := t.Context()
	g := kindGate{profiles: internalErrProfiles{memory.NewDeviceProfileStore()}}
	if _, _, err := g.effectiveKind(ctx, kgInternalLFDI); err == nil {
		t.Error("effectiveKind: profile lookup error was swallowed")
	}
	g = kindGate{managers: internalErrPairs{}}
	if _, _, err := g.effectiveKind(ctx, kgInternalLFDI); err == nil {
		t.Error("effectiveKind: management lookup error was swallowed")
	}
	if msg, err := g.managerRefusal(ctx, kgInternalLFDI); err == nil {
		t.Errorf("managerRefusal = %q, nil; want the lookup error", msg)
	}
	if msg, err := g.managedRefusal(ctx, kgInternalLFDI); err == nil {
		t.Errorf("managedRefusal = %q, nil; want the lookup error", msg)
	}
	for _, kind := range []string{"aggregator", "device"} {
		if msg, err := g.profileKindRefusal(ctx, kgInternalLFDI, kind); err == nil {
			t.Errorf("profileKindRefusal(%s) = %q, nil; want the lookup error", kind, msg)
		}
	}
}
