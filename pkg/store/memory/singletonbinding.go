package memory

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// SingletonLinks says which of the three per-device singletons the server
// serves, and so which links an EndDevice advertises.
type SingletonLinks struct {
	Configuration bool
	DeviceStatus  bool
	PowerStatus   bool
}

// SingletonLinkedEndDeviceStore decorates any [store.EndDeviceStore] so every
// EndDevice it serves links the singletons in its [SingletonLinks] and no
// others (IEEE 2030.5-2018 section 4.4). Links are derived on every read and
// cleared on every write, so a client cannot install one. The records are
// cascaded on Delete by [DeviceKeyedCascadeEndDeviceStore], not here.
type SingletonLinkedEndDeviceStore struct {
	linkedEndDevices
}

var _ store.EndDeviceStore = (*SingletonLinkedEndDeviceStore)(nil)

type singletonLinks SingletonLinks

func (l singletonLinks) derive(_ context.Context, d *sep2.EndDevice, key string) {
	d.ConfigurationLink, d.DeviceStatusLink, d.PowerStatusLink = nil, nil, nil
	if l.Configuration {
		d.ConfigurationLink = &sep2.Link{Href: "/edev/" + key + "/cfg"}
	}
	if l.DeviceStatus {
		d.DeviceStatusLink = &sep2.Link{Href: "/edev/" + key + "/dstat"}
	}
	if l.PowerStatus {
		d.PowerStatusLink = &sep2.Link{Href: "/edev/" + key + "/ps"}
	}
}

func (singletonLinks) forWrite(_ context.Context, d *sep2.EndDevice, _ string) {
	d.ConfigurationLink, d.DeviceStatusLink, d.PowerStatusLink = nil, nil, nil
}

func (singletonLinks) strip(d *sep2.EndDevice) bool {
	had := d.ConfigurationLink != nil || d.DeviceStatusLink != nil || d.PowerStatusLink != nil
	d.ConfigurationLink, d.DeviceStatusLink, d.PowerStatusLink = nil, nil, nil
	return had
}

func (singletonLinks) linkNames() string {
	return "ConfigurationLink, DeviceStatusLink, PowerStatusLink"
}

// NewSingletonLinkedEndDeviceStore decorates devs, which is required.
func NewSingletonLinkedEndDeviceStore(devs store.EndDeviceStore, links SingletonLinks) *SingletonLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewSingletonLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	return &SingletonLinkedEndDeviceStore{linkedEndDevices: linkedEndDevices{devs: devs, fields: singletonLinks(links)}}
}
