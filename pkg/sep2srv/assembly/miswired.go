package assembly

import (
	"context"
	"fmt"
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Mis-wired store handles: what happens when a family arrives half-wired.
//
// # The gates are per family, and the fix is not to make them per field
//
// Thirteen mount decisions cover roughly fifteen handles. Fifteen fields have
// no gate of their own: DERCapabilities, DERSettings, DERStatuses,
// DERAvailabilities, DERPrograms, DERControls, DefaultDERControls and DERCurves
// ride on DERs; MirrorMeterReadings rides on MirrorUsagePoints; MeterReadings,
// Readings and ReadingTypes ride on UsagePoints; TextMessages rides on
// MessagingPrograms; FlowReservationResponses rides on
// FlowReservationRequests; Responses rides on ResponseSets. Nothing forced the
// siblings to arrive together, so a Stores holding EndDevices and DERs with
// DERPrograms unset mounted GET /edev/{id}/fsa/{fsaId}/derp over a nil handle
// and the first request dereferenced it: an EOF to the client, a stack trace
// and no diagnosis to the operator.
//
// Gating each field on its own handle was considered and rejected, because it
// trades the panic for the defect the mintable-href ratchet exists to abolish.
// The links that point at these routes are minted by handler packages that
// consult no store at all: handlers/fsa.HandleFSA stamps DERProgramListLink on
// every FSA it serves, handlers/metering stamps MeterReadingListLink on every
// UsagePoint, handlers/response stamps ResponseListLink on every ResponseSet,
// and MintableHrefs in hrefs.go names each of those minters as the SOURCE of
// the route in question. Unmounting the route while its minter keeps minting
// is advertised-but-unrouted, the same defect class this package's mintable-
// href assertion exists to abolish. Making per-field gating safe would mean
// threading a link policy into five handler packages so each minter could
// be silenced with its route, which is a far wider change than the defect
// warrants and one that must be designed rather than slipped in.
//
// So the doctrine is stated honestly instead: THE GATE IS PER FAMILY, the
// anchor field is the whole family's gate, and every other handle in a wired
// family is REQUIRED. A family whose anchor is present and whose member is not
// is a mis-wired deployment, and this file is what makes that fact say so.
//
// # What a mis-wired handle does instead of panicking
//
// It is replaced by a store that refuses every operation with a descriptive
// error naming the field, and the substitution is logged once at assembly. The
// route stays mounted, so nothing that was advertised becomes unrouted, and the
// request renders as a 500 through the same srverr path any other store failure
// takes.
//
// A 500 is the correct answer and an empty list is not. Both a route that is
// removed and a store that answers empty tell the client a FACT about the
// fleet, that the resource is absent, which a mis-wired server has not
// established and is not entitled to assert. This is the same "absent is not
// the same as failed" rule the store error contract states at every method, and
// the same reasoning applied elsewhere across the route surface.
//
// The refusal is deliberately NOT a panic at BuildProtocolRouter either.
// Constructors that take one required collaborator panic, and should: the
// caller named the argument. BuildProtocolRouter takes a whole struct, is
// already documented to substitute and log for a nil EndDeviceIndexes and a nil
// AuthPolicy.Wrap, and is called by consumers outside this repository. Turning
// a partially-wired Stores into a boot-time crash is a behaviour change to a
// published entry point; refusing one function set while every other one keeps
// serving is the proportionate answer and still leaves the operator a log line
// per mis-wired field.

// requireScoped returns handle, or a refusing substitute when the handle is
// absent while its family is mounted.
//
// field names the Stores field so the log line and every error the substitute
// returns point the operator at the exact handle to wire, rather than at the
// route that happened to be requested first.
func requireScoped[T store.Copier[T]](handle store.ScopedStore[T], field string) store.ScopedStore[T] {
	if !store.IsAbsent(handle) {
		return handle
	}
	log.Printf("assembly: Stores.%s is not wired but the function set that mounts its routes is: "+
		"those routes will answer 500 until the handle is supplied", field)
	return miswiredScopedStore[T]{field: field}
}

// requireResource is [requireScoped] for the flat half of the store contract.
func requireResource[T store.Copier[T]](handle store.ResourceStore[T], field string) store.ResourceStore[T] {
	if !store.IsAbsent(handle) {
		return handle
	}
	log.Printf("assembly: Stores.%s is not wired but the function set that mounts its routes is: "+
		"those routes will answer 500 until the handle is supplied", field)
	return miswiredResourceStore[T]{field: field}
}

// requireEndDevices is [requireResource] for the anchor of every /edev route.
// EndDevices has no family gate above it, because /edev is the root of the
// discovery walk and mounts whenever Stores does, so an absent handle refuses
// here instead: POST /edev and GET /edev are exempt from the ownership gate and
// would otherwise reach the handle unguarded.
func requireEndDevices(handle store.EndDeviceStore) store.EndDeviceStore {
	if !store.IsAbsent(handle) {
		return handle
	}
	log.Print("assembly: Stores.EndDevices is not wired: every /edev route, POST /edev included, " +
		"will answer 500 until the handle is supplied")
	return miswiredEndDeviceStore{miswiredResourceStore[sep2.EndDevice]{field: "EndDevices"}}
}

// miswiredErr is the error every refusing store returns.
//
// It wraps nothing: in particular it is not store.ErrNotFound, because a
// mis-wired server has not established that anything is absent, and it is not
// store.ErrAlreadyExists, because the upsert idiom in this package branches on
// that straight into an Update and would turn one refused write into two.
func miswiredErr(field string) error {
	return fmt.Errorf("assembly: Stores.%s is not wired; this server cannot serve this function set", field)
}

// miswiredScopedStore refuses every operation of [store.ScopedStore].
type miswiredScopedStore[T store.Copier[T]] struct{ field string }

func (s miswiredScopedStore[T]) Get(_ context.Context, _, _ string) (T, error) {
	var zero T
	return zero, miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) List(_ context.Context, _ string, _ store.ListOptions) (store.ListResult[T], error) {
	return store.ListResult[T]{}, miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) Count(_ context.Context, _ string) (uint32, error) {
	return 0, miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) HasParent(_ context.Context, _ string) (bool, error) {
	return false, miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) Parents(_ context.Context) ([]string, error) {
	return nil, miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) Create(_ context.Context, _, _ string, _ T) error {
	return miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) Update(_ context.Context, _, _ string, _ T) error {
	return miswiredErr(s.field)
}

func (s miswiredScopedStore[T]) Delete(_ context.Context, _, _ string) error {
	return miswiredErr(s.field)
}

// miswiredResourceStore refuses every operation of [store.ResourceStore].
type miswiredResourceStore[T store.Copier[T]] struct{ field string }

func (s miswiredResourceStore[T]) Get(_ context.Context, _ string) (T, error) {
	var zero T
	return zero, miswiredErr(s.field)
}

func (s miswiredResourceStore[T]) List(_ context.Context, _ store.ListOptions) (store.ListResult[T], error) {
	return store.ListResult[T]{}, miswiredErr(s.field)
}

func (s miswiredResourceStore[T]) Count(_ context.Context) (uint32, error) {
	return 0, miswiredErr(s.field)
}

func (s miswiredResourceStore[T]) Create(_ context.Context, _ string, _ T) error {
	return miswiredErr(s.field)
}

func (s miswiredResourceStore[T]) Update(_ context.Context, _ string, _ T) error {
	return miswiredErr(s.field)
}

func (s miswiredResourceStore[T]) Delete(_ context.Context, _ string) error {
	return miswiredErr(s.field)
}

// miswiredEndDeviceStore refuses every operation of [store.EndDeviceStore].
type miswiredEndDeviceStore struct {
	miswiredResourceStore[sep2.EndDevice]
}

func (s miswiredEndDeviceStore) GetBySFDI(_ context.Context, _ string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, miswiredErr(s.field)
}

func (s miswiredEndDeviceStore) GetByLFDI(_ context.Context, _ string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, miswiredErr(s.field)
}
