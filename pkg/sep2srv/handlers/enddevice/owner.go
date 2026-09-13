package enddevice

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// OwnedBy reports whether a caller presenting callerLFDI owns the EndDevice
// whose stored LFDI is storedLFDI.
//
// It is true only when both are non-empty and exactly equal, with no case
// folding. Empty operands are refused here even when a caller has already
// checked them, so a record stored without an LFDI is owned by nobody.
func OwnedBy(storedLFDI, callerLFDI string) bool {
	return storedLFDI != "" && callerLFDI != "" && storedLFDI == callerLFDI
}

// HandleEndDeviceListForCaller returns a handler for GET /edev that lists the
// requesting certificate's own EndDevice and every EndDevice it manages.
//
// Each device is found with one GetByLFDI lookup: the caller's LFDI, then each
// LFDI managers.ManagedBy returns. An absent managers store lists the caller's
// own device only. A record that grants nothing is skipped and logged: a
// managed LFDI with no record (logged once until it resolves again), a record
// the LFDI index returned for a different LFDI, and a record whose href names
// no store key, which no client could address. A failing store is a 500,
// because a list that cannot be built truthfully must not be served as though
// it were complete.
//
// The set is ordered by store key, the segment after /edev/ in each record's
// href, and s, l and a page over it. All counts the whole set and Results the
// page, never the store's total: a client pages by those counts. A caller with
// nothing to list gets a well-formed empty list rather than a 404, because a
// device part way through registration still discovers itself through this
// collection.
//
// A request with no identity, or an empty LFDI, is refused with 403, the same
// answer the /edev/{id} ownership gate gives it. An empty list would instead
// assert "you have no EndDevice" about an identity the server never received.
func HandleEndDeviceListForCaller(s store.EndDeviceStore, managers store.EndDeviceManagementStore, identity IdentityFunc, pollRate uint32) http.HandlerFunc {
	reported := &onceLog{}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		if identity == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		callerLFDI, _, ok := identity(r.Context())
		if !ok || callerLFDI == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if store.IsAbsent(s) {
			srverr.Internal(w, r, errors.New("enddevice: no EndDevice store is wired"))
			return
		}

		devices, err := callerDevices(r, s, managers, callerLFDI, reported)
		if err != nil {
			srverr.Internal(w, r, err)
			return
		}
		result := page(devices, paging.ParseQuery(r.URL.Query()))
		encoding.WriteXML(w, http.StatusOK, BuildEndDeviceList(r.URL.Path, result, pollRate))
	}
}

// onceLog writes a line for a key only the first time the key is seen, so a
// list a manager polls does not repeat the same skip on every request.
type onceLog struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (o *onceLog) printf(key, format string, args ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen[key] {
		return
	}
	if o.seen == nil {
		o.seen = make(map[string]bool)
	}
	o.seen[key] = true
	log.Printf(format, args...)
}

// forget lets key be logged again once the condition it reported has cleared.
func (o *onceLog) forget(key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.seen, key)
}

// listedDevice is one EndDevice in a caller's list with the store key it is
// ordered and paged by.
type listedDevice struct {
	key string
	dev sep2.EndDevice
}

// callerDevices resolves the caller's own EndDevice and those it manages,
// sorted by store key.
func callerDevices(r *http.Request, s store.EndDeviceStore, managers store.EndDeviceManagementStore, callerLFDI string, reported *onceLog) ([]listedDevice, error) {
	lfdis := []string{callerLFDI}
	if !store.IsAbsent(managers) {
		managed, err := managers.ManagedBy(r.Context(), callerLFDI)
		if err != nil {
			return nil, fmt.Errorf("list the EndDevices the caller manages: %w", err)
		}
		lfdis = append(lfdis, managed...)
	}

	var devices []listedDevice
	for i, lfdi := range lfdis {
		noRecord := "no-record:" + lfdi
		dev, err := s.GetByLFDI(r.Context(), lfdi)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if i > 0 {
				reported.printf(noRecord, "enddevice: %s: managed LFDI %s has no EndDevice record; not listed", srverr.Route(r), lfdi)
			}
			continue
		case err != nil:
			return nil, fmt.Errorf("look up an EndDevice by LFDI: %w", err)
		case !OwnedBy(dev.LFDI, lfdi):
			// An index that drifted from its records lists nothing rather
			// than another device.
			key, _ := strings.CutPrefix(dev.Href, "/edev/")
			log.Printf("enddevice: %s: the LFDI index returned the record at store key %q, whose stored LFDI differs; not listed", srverr.Route(r), key)
			continue
		}
		reported.forget(noRecord)

		key, ok := strings.CutPrefix(dev.Href, "/edev/")
		if !ok || key == "" || strings.Contains(key, "/") {
			// Left out rather than failing the list: no client could address
			// it, and one bad record must not cost a manager its other devices.
			reported.printf("malformed:"+dev.Href, "enddevice: %s: an EndDevice in the caller's list has href %q, which names no store key; not listed", srverr.Route(r), dev.Href)
			continue
		}
		devices = append(devices, listedDevice{key: key, dev: dev})
	}
	slices.SortFunc(devices, func(a, b listedDevice) int { return strings.Compare(a.key, b.key) })
	return devices, nil
}

// page applies s, l and a to devices, sorted by store key, with the same
// semantics as the stores' own List: a keeps keys strictly after it, s skips
// from there, and l of 0 returns no items.
func page(devices []listedDevice, p paging.Params) store.ListResult[sep2.EndDevice] {
	result := store.ListResult[sep2.EndDevice]{All: uint32(len(devices))}
	if p.After != "" {
		i := slices.IndexFunc(devices, func(d listedDevice) bool { return d.key > p.After })
		if i < 0 {
			i = len(devices)
		}
		devices = devices[i:]
	}
	if p.Limit == 0 || p.Start >= uint32(len(devices)) {
		return result
	}
	devices = devices[p.Start:]
	if uint32(len(devices)) > p.Limit {
		devices = devices[:p.Limit]
	}
	for _, d := range devices {
		result.Items = append(result.Items, d.dev)
	}
	result.Results = uint32(len(result.Items))
	return result
}
