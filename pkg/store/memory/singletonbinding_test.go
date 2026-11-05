package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestSingletonLinkedEndDeviceStoreDerivesAndStripsLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	inner := memory.NewEndDeviceStore()
	s := memory.NewSingletonLinkedEndDeviceStore(inner, memory.SingletonLinks{Configuration: true, PowerStatus: true})

	forged := sep2.EndDevice{DeviceStatusLink: &sep2.Link{Href: "/forged"}, ConfigurationLink: &sep2.Link{Href: "/forged"}}
	forged.Href = "/edev/7"
	if err := s.Create(ctx, "7", forged); err != nil {
		t.Fatal(err)
	}

	stored, err := inner.Get(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfigurationLink != nil || stored.DeviceStatusLink != nil || stored.PowerStatusLink != nil {
		t.Errorf("a client-supplied link reached the store: %+v", stored)
	}

	got, err := s.Get(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigurationLink == nil || got.ConfigurationLink.Href != "/edev/7/cfg" {
		t.Errorf("ConfigurationLink = %+v, want /edev/7/cfg", got.ConfigurationLink)
	}
	if got.PowerStatusLink == nil || got.PowerStatusLink.Href != "/edev/7/ps" {
		t.Errorf("PowerStatusLink = %+v, want /edev/7/ps", got.PowerStatusLink)
	}
	if got.DeviceStatusLink != nil {
		t.Errorf("DeviceStatusLink = %+v, want nil: device status is not served", got.DeviceStatusLink)
	}
}
