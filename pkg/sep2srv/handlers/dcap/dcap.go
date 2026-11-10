package dcap

import (
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

// link maps a family prefix to the DeviceCapability element that advertises
// it. Whether the family is served comes from [routefamily.Wired], so this
// table decides only where a served family's link is written.
type link struct {
	prefix string
	set    func(dc *sep2.DeviceCapability, all uint32, hasAll bool)
}

func listLink(prefix string, field func(*sep2.DeviceCapability) **sep2.ListLink) link {
	return link{prefix: prefix, set: func(dc *sep2.DeviceCapability, all uint32, hasAll bool) {
		l := &sep2.ListLink{Href: prefix}
		if hasAll {
			l.All = all
		}
		*field(dc) = l
	}}
}

func singleLink(prefix string, field func(*sep2.DeviceCapability) **sep2.Link) link {
	return link{prefix: prefix, set: func(dc *sep2.DeviceCapability, _ uint32, _ bool) {
		*field(dc) = &sep2.Link{Href: prefix}
	}}
}

// links is in XSD order: FunctionSetAssignmentsBase elements, then the
// DeviceCapability extension elements.
var links = []link{
	listLink("/msg", func(dc *sep2.DeviceCapability) **sep2.ListLink { return &dc.MessagingProgramListLink }),
	listLink("/rsps", func(dc *sep2.DeviceCapability) **sep2.ListLink { return &dc.ResponseSetListLink }),
	singleLink("/tm", func(dc *sep2.DeviceCapability) **sep2.Link { return &dc.TimeLink }),
	listLink("/upt", func(dc *sep2.DeviceCapability) **sep2.ListLink { return &dc.UsagePointListLink }),
	listLink("/edev", func(dc *sep2.DeviceCapability) **sep2.ListLink { return &dc.EndDeviceListLink }),
	listLink("/mup", func(dc *sep2.DeviceCapability) **sep2.ListLink { return &dc.MirrorUsagePointListLink }),
	singleLink("/sdev", func(dc *sep2.DeviceCapability) **sep2.Link { return &dc.SelfDeviceLink }),
}

// CallerEndDevices counts the EndDevices the caller of r can list. ok is
// false when the caller has no identity, so no count is claimed for it.
type CallerEndDevices func(r *http.Request) (all uint32, ok bool, err error)

// countBudget bounds the per-caller count. Every client polls /dcap, so a
// management store that stalls must cost the link its all and never hold the
// response.
const countBudget = 250 * time.Millisecond

var errCountTimeout = errors.New("the EndDevice count took longer than its budget")

type counted struct {
	all uint32
	ok  bool
	err error
}

// errCountBusy reports that an earlier count has not returned yet.
var errCountBusy = errors.New("an earlier EndDevice count is still running")

// boundedCount runs count for at most countBudget. Only one count is in
// flight per handler: a store that ignores its context keeps that one
// goroutine, and later polls skip the count instead of adding another.
func boundedCount(inflight *atomic.Bool, count CallerEndDevices, r *http.Request) (uint32, bool, error) {
	if !inflight.CompareAndSwap(false, true) {
		return 0, false, errCountBusy
	}
	done := make(chan counted, 1)
	go func() {
		defer inflight.Store(false)
		all, ok, err := count(r)
		done <- counted{all, ok, err}
	}()
	select {
	case c := <-done:
		return c.all, c.ok, c.err
	case <-time.After(countBudget):
		return 0, false, errCountTimeout
	}
}

// HandleDeviceCapability returns a handler for GET /dcap, the entry point of
// the discovery walk. It links exactly the families wired serves, because
// IEEE 2030.5-2018 section 4.4 forbids a Link to a function set the server
// does not implement.
//
// The EndDeviceListLink all is the number of devices GET /edev lists for this
// caller, so it never reveals another caller's devices. A nil endDevices, a
// caller with no identity or a failed count leave all unset.
//
// DERProgramListLink is not linked: core has no top-level DERProgramList, and
// "/dc" is the DERCurve list.
func HandleDeviceCapability(wired routefamily.Wired, endDevices CallerEndDevices) http.HandlerFunc {
	var inflight atomic.Bool
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		dc := sep2.DeviceCapability{Resource: sep2.Resource{Href: "/dcap"}, PollRate: 900}
		for _, l := range links {
			if !wired.Serves(l.prefix) {
				continue
			}
			var all uint32
			var hasAll bool
			if l.prefix == "/edev" && endDevices != nil {
				n, ok, err := boundedCount(&inflight, endDevices, r)
				if err != nil {
					log.Printf("dcap: EndDeviceListLink served without all: %s", logsafe.Err(err))
				}
				all, hasAll = n, ok && err == nil
			}
			l.set(&dc, all, hasAll)
		}
		encoding.WriteXML(w, http.StatusOK, &dc)
	}
}
