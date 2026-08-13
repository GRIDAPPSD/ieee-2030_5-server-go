package der

import (
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DER sub-resource link derivation.
//
// # Why derivation happens at serve time
//
// IEEE 2030.5-2018 section 4.4 p.19: "If a function set is not implemented,
// Link elements to resources in that function set SHALL NOT be included." That
// makes a link to a URI this server does not serve a conformance violation
// rather than untidiness, and an omitted link the correct signal that a function
// set is absent rather than a gap to be filled defensively.
//
// A seeder structurally cannot honour that rule, because it does not know what
// the router mounted. Serve time is the only layer where "which links are legal
// to emit" is knowable, which is why the policy below is built by the code that
// registers the routes and applied here rather than stamped upstream.
//
// The obligation runs in the other direction too: 2018 section 10.10.5 p.125,
// "Each unique DER instance SHALL link to a DERCapability instance." Both
// obligations are prose. sep.xsd declares all seven DER link fields
// minOccurs="0", so the schema gate can catch neither and a test must.
//
// # Why each href is spelled out rather than tabulated
//
// The four fills below repeat base + a literal suffix instead of ranging over a
// table. That repetition is load-bearing: the mintable-href source scan
// (pkg/sep2srv/assembly/hrefsource_test.go) folds a literal-rooted concatenation
// into a shape and fails when the shape is not declared and routed, and it
// cannot fold a suffix read out of a struct field. A table would put these
// hrefs beyond the reach of the guard that exists to keep exactly this kind of
// minting honest, so the guard wins over the abstraction.

// DERLinkPolicy declares which DER sub-resource links this server is permitted
// to put on the wire.
//
// A field exists here only when [FillAbsentDERLinks] can derive the link AND a
// route serves it. That coupling is the point rather than an omission: a policy
// bit with nothing behind it is a knob that does nothing, and a derivation with
// no route mints an href the mintable-href assertion rejects
// (pkg/sep2srv/assembly/hrefs.go). A card that mounts another DER sub-resource
// adds its field, its derivation, its MintableHrefs declaration and its mount in
// ONE commit, because under section 4.4 mounting and advertising are a single
// act.
//
// AssociatedDERProgramList, CurrentDERProgram and AssociatedUsagePoint are
// absent for exactly that reason: nothing yet owns their routes, and the
// last two have no field on [sep2.DER] yet either.
type DERLinkPolicy struct {
	// Capability gates DERCapabilityLink, which 2018 section 10.10.5 requires
	// on every served DER. A router that mounts the DER function set at all
	// mounts this sub-resource, so it is true in practice; it is a field rather
	// than an assumption so a consumer wiring a partial store set cannot
	// advertise a URI it does not serve.
	Capability bool
	// Settings gates DERSettingsLink.
	Settings bool
	// Status gates DERStatusLink.
	Status bool
	// Availability gates DERAvailabilityLink.
	Availability bool
}

// FillAbsentDERLinks stamps the permitted sub-resource links onto d for every
// permitted link field that is nil, deriving each href from base, which is the
// DER's own canonical href.
//
// Fill-ABSENT, never overwrite. A link already present is left exactly as
// stored, and a stored link that disagrees with what would have been derived is
// LOGGED rather than corrected. Overwriting would hide a real upstream defect
// behind a correct-looking response, and a stored href is upstream identity: the
// data-invariants rule that forbids replacing an upstream value with a locally
// synthesized one applies directly. The disagreement becomes observable; the
// data is not rewritten on a read path.
//
// A blank base refuses the whole pass. The derived href would be "/dercap" and
// friends, which route nowhere, so the server would advertise a URI no client
// can follow. Refusing and logging is fail-closed; deriving an id from the
// enclosing list's path would be the invisible corruption the refusal prevents.
//
// One case is deliberately NOT handled: a stored link pointing at a function set
// that is not mounted. That is a live section 4.4 violation, and stripping it
// here would make a read path rewrite stored data. It is tracked as future
// work, and it cannot fire today because the only links anything stamps are
// the mounted four.
func FillAbsentDERLinks(d *sep2.DER, base string, p DERLinkPolicy) {
	if d == nil {
		return
	}
	if base == "" {
		log.Printf("der: a DER with no href cannot have its sub-resource links derived; leaving them absent")
		return
	}

	// Ordered as the sep.xsd DER sequence orders the fields, so a reader
	// comparing this against the struct is comparing like with like.
	fillLink(&d.DERAvailabilityLink, p.Availability, base+"/dera", "DERAvailabilityLink", base)
	fillLink(&d.DERCapabilityLink, p.Capability, base+"/dercap", "DERCapabilityLink", base)
	fillLink(&d.DERSettingsLink, p.Settings, base+"/derg", "DERSettingsLink", base)
	fillLink(&d.DERStatusLink, p.Status, base+"/ders", "DERStatusLink", base)
}

// fillLink applies the fill-absent rule to one link field. want is the href the
// field would derive to; field and base name the subject of the log line, since
// "a link disagrees" without saying which link, and under which DER, leaves the
// reader grepping.
func fillLink(dst **sep2.Link, permitted bool, want, field, base string) {
	if !permitted {
		return
	}
	if *dst == nil {
		*dst = &sep2.Link{Href: want}
		return
	}
	if (*dst).Href != want {
		log.Printf("der: %s is stored as %q but derives to %q under %q; serving the stored value",
			field, (*dst).Href, want, base)
	}
}

// DropUnpermittedDERLinks clears every link on d this server is not permitted to
// advertise.
//
// It runs on the PUT path ONLY, where d is a client-supplied document. A client
// must not be able to inject a link to a function set we do not serve: our own
// subsequent GET would then be nonconformant under section 4.4 on the client's
// behalf. Dropping rather than rejecting with a 400 is the deliberate choice for
// now: a 400 on a field the client believes is optional is a worse interop
// failure than a dropped link, and every drop is logged, so a client that trips
// it is visible rather than silently accommodated.
//
// This is NOT applied on the read path. Suppressing stored data while serving it
// would hide an upstream defect exactly the way overwriting a link would.
func DropUnpermittedDERLinks(d *sep2.DER, p DERLinkPolicy) {
	if d == nil {
		return
	}

	dropLink(&d.DERAvailabilityLink, p.Availability, "DERAvailabilityLink")
	dropLink(&d.DERCapabilityLink, p.Capability, "DERCapabilityLink")
	dropLink(&d.DERSettingsLink, p.Settings, "DERSettingsLink")
	dropLink(&d.DERStatusLink, p.Status, "DERStatusLink")

	// AssociatedDERProgramListLink has no policy field because nothing derives
	// it and no route serves /edev/{id}/der/{derId}/derp yet, so it is never
	// permitted. When that lands it gains a field, a fill and a mount
	// together, and this special case goes away.
	if d.AssociatedDERProgramListLink != nil {
		log.Printf("der: dropping client-supplied AssociatedDERProgramListLink %q: no route serves the associated DERProgram list yet",
			d.AssociatedDERProgramListLink.Href)
		d.AssociatedDERProgramListLink = nil
	}
}

// dropLink clears one link field when its function set is not served.
func dropLink(dst **sep2.Link, permitted bool, field string) {
	if permitted || *dst == nil {
		return
	}
	log.Printf("der: dropping client-supplied %s %q: this server does not serve that function set (2018 section 4.4)",
		field, (*dst).Href)
	*dst = nil
}

// StampDERInstance returns the serve-time completion applied to a DER on the
// instance route, for GET, HEAD and PUT alike.
//
// The request path is the DER's canonical href by construction, so it is the
// base every derived link is built from and no store plumbing is needed to find
// one. The methods differ in exactly one respect, and the difference is about
// who owns the value:
//
//   - On PUT the Href is re-stamped from the request path unconditionally. A
//     client must not be able to write a DER whose own href disagrees with the
//     URI it was written to, the same reasoning that makes the mirror path
//     override a client-supplied Href server-side.
//   - On GET a stored Href WINS, because it is upstream data rather than client
//     input. A disagreement with the request path is logged, not corrected.
func StampDERInstance(p DERLinkPolicy) func(r *http.Request, d *sep2.DER) {
	return func(r *http.Request, d *sep2.DER) {
		if d == nil {
			return
		}
		base := r.URL.Path

		if r.Method == http.MethodPut {
			if d.Href != "" && d.Href != base {
				log.Printf("der: PUT %s carried Href %q; re-stamping it from the request path", base, d.Href)
			}
			d.Href = base
			DropUnpermittedDERLinks(d, p)
		} else if d.Href == "" {
			d.Href = base
		} else if d.Href != base {
			log.Printf("der: the DER stored at %s has Href %q; serving the stored value", base, d.Href)
		}

		FillAbsentDERLinks(d, base, p)
	}
}

// DERListBuilder returns a DERList builder that completes each member's
// sub-resource links under p before the list goes on the wire.
//
// The list route and the instance route derive links through one function for
// the same reason deepScopeKey is one function rather than a repeated
// expression: if one filled links and the other did not, a single DER would
// serialize differently depending on which route reached it, and a client
// caching by href would hold two documents for one resource.
//
// Each member's base is its OWN href, not the list's. A member with no href is
// left untouched and logged by [FillAbsentDERLinks]: the list's path cannot name
// the member's id, so any base synthesized here would be a guess.
//
// The members are completed in place, so result.Items is written through. That
// is safe for every caller today because a store List returns deep copies and
// the page is serialized and discarded, but a caller that intends to reuse the
// page afterwards should copy it first.
func DERListBuilder(p DERLinkPolicy) func(href string, result store.ListResult[sep2.DER], pollRate uint32) sep2.DERList {
	return func(href string, result store.ListResult[sep2.DER], pollRate uint32) sep2.DERList {
		list := BuildDERList(href, result, pollRate)
		for i := range list.DER {
			FillAbsentDERLinks(&list.DER[i], list.DER[i].Href, p)
		}
		return list
	}
}
