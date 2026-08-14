// Tests for DER sub-resource link derivation.
//
// The rules under test are prose obligations the schema cannot express: 2018
// section 4.4 p.19 forbids a link to an unimplemented function set, and section
// 10.10.5 p.125 requires a DERCapability link on every DER. sep.xsd declares all
// seven link fields minOccurs="0", so the schema gate has nothing to check here
// and these assertions are the only thing holding either line.
package der_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coredel "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// storeResult wraps DERs as a store page, the shape a list builder is handed.
func storeResult(ders ...sep2.DER) store.ListResult[sep2.DER] {
	return store.ListResult[sep2.DER]{
		All:     uint32(len(ders)),
		Results: uint32(len(ders)),
		Items:   ders,
	}
}

// allMounted is the phase-1 policy: the four sub-resources the router mounts.
var allMounted = coredel.DERLinkPolicy{Capability: true, Settings: true, Status: true, Availability: true}

const testBase = "/edev/7/der/3"

func TestFillAbsentDERLinks(t *testing.T) {
	t.Parallel()

	const foreign = "https://other.example/edev/7/der/3/dercap"

	tests := []struct {
		name   string
		in     sep2.DER
		base   string
		policy coredel.DERLinkPolicy
		want   map[string]string // field -> href, "" meaning the field must be absent
	}{
		{
			name:   "an empty DER gains every permitted link",
			base:   testBase,
			policy: allMounted,
			want: map[string]string{
				"DERAvailabilityLink": testBase + "/dera",
				"DERCapabilityLink":   testBase + "/dercap",
				"DERSettingsLink":     testBase + "/derg",
				"DERStatusLink":       testBase + "/ders",
			},
		},
		{
			name: "a stored link that disagrees is kept, not corrected",
			in: sep2.DER{
				DERCapabilityLink: &sep2.Link{Href: foreign},
			},
			base:   testBase,
			policy: allMounted,
			want: map[string]string{
				// The stored value wins: overwriting would hide a real upstream
				// defect behind a correct-looking response.
				"DERCapabilityLink":   foreign,
				"DERAvailabilityLink": testBase + "/dera",
				"DERSettingsLink":     testBase + "/derg",
				"DERStatusLink":       testBase + "/ders",
			},
		},
		{
			name:   "a link is not emitted for a function set the policy does not permit",
			base:   testBase,
			policy: coredel.DERLinkPolicy{Capability: true},
			want: map[string]string{
				"DERCapabilityLink":   testBase + "/dercap",
				"DERAvailabilityLink": "",
				"DERSettingsLink":     "",
				"DERStatusLink":       "",
			},
		},
		{
			name: "a stored link to an unpermitted function set is left in place",
			in: sep2.DER{
				DERStatusLink: &sep2.Link{Href: testBase + "/ders"},
			},
			base:   testBase,
			policy: coredel.DERLinkPolicy{Capability: true},
			want: map[string]string{
				"DERCapabilityLink": testBase + "/dercap",
				// Stripping stored data on a read path is a separate
				// decision to make, not this function's.
				"DERStatusLink": testBase + "/ders",
			},
		},
		{
			name:   "a blank base derives nothing rather than minting a rootless href",
			base:   "",
			policy: allMounted,
			want: map[string]string{
				"DERAvailabilityLink": "",
				"DERCapabilityLink":   "",
				"DERSettingsLink":     "",
				"DERStatusLink":       "",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.in
			coredel.FillAbsentDERLinks(&got, tc.base, tc.policy)
			assertLinks(t, got, tc.want)
		})
	}
}

func TestDropUnpermittedDERLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     sep2.DER
		policy coredel.DERLinkPolicy
		want   map[string]string
	}{
		{
			name: "a client-supplied link to an unserved function set is dropped",
			in: sep2.DER{
				DERCapabilityLink: &sep2.Link{Href: testBase + "/dercap"},
				DERStatusLink:     &sep2.Link{Href: testBase + "/ders"},
			},
			policy: coredel.DERLinkPolicy{Capability: true},
			want: map[string]string{
				"DERCapabilityLink": testBase + "/dercap",
				"DERStatusLink":     "",
			},
		},
		{
			name: "AssociatedDERProgramListLink is never accepted while no route serves it",
			in: sep2.DER{
				AssociatedDERProgramListLink: &sep2.ListLink{Href: testBase + "/derp"},
				DERCapabilityLink:            &sep2.Link{Href: testBase + "/dercap"},
			},
			policy: allMounted,
			want: map[string]string{
				"AssociatedDERProgramListLink": "",
				"DERCapabilityLink":            testBase + "/dercap",
			},
		},
		{
			name: "permitted links survive untouched",
			in: sep2.DER{
				DERAvailabilityLink: &sep2.Link{Href: testBase + "/dera"},
				DERCapabilityLink:   &sep2.Link{Href: testBase + "/dercap"},
				DERSettingsLink:     &sep2.Link{Href: testBase + "/derg"},
				DERStatusLink:       &sep2.Link{Href: testBase + "/ders"},
			},
			policy: allMounted,
			want: map[string]string{
				"DERAvailabilityLink": testBase + "/dera",
				"DERCapabilityLink":   testBase + "/dercap",
				"DERSettingsLink":     testBase + "/derg",
				"DERStatusLink":       testBase + "/ders",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.in
			coredel.DropUnpermittedDERLinks(&got, tc.policy)
			assertLinks(t, got, tc.want)
		})
	}
}

// TestStampDERInstance_HrefOwnership is the difference between the read path and
// the write path stated as an assertion: a stored href is upstream data and
// survives, a client-supplied href is input and does not.
func TestStampDERInstance_HrefOwnership(t *testing.T) {
	t.Parallel()

	stamp := coredel.StampDERInstance(allMounted)

	tests := []struct {
		name     string
		method   string
		storedIn string
		want     string
	}{
		{
			name:     "GET keeps a stored href that disagrees with the request path",
			method:   http.MethodGet,
			storedIn: "/edev/99/der/99",
			want:     "/edev/99/der/99",
		},
		{
			name:   "GET stamps an absent href from the request path",
			method: http.MethodGet,
			want:   testBase,
		},
		{
			name:     "PUT overrides a client-supplied href with the URI it was written to",
			method:   http.MethodPut,
			storedIn: "/edev/99/der/99",
			want:     testBase,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(tc.method, testBase, nil)
			d := sep2.DER{SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: tc.storedIn},
			}}
			stamp(r, &d)

			if d.Href != tc.want {
				t.Errorf("Href = %q, want %q", d.Href, tc.want)
			}
			// Whichever path was taken, the links derive from the request path,
			// which is the resource's canonical URI on this route.
			if d.DERCapabilityLink == nil {
				t.Fatal("DERCapabilityLink is absent; 2018 section 10.10.5 requires it on every DER")
			}
			if want := testBase + "/dercap"; d.DERCapabilityLink.Href != want {
				t.Errorf("DERCapabilityLink.Href = %q, want %q", d.DERCapabilityLink.Href, want)
			}
		})
	}
}

// TestStampDERInstance_DropsInjectedLinksOnPUTOnly pins that sanitisation is a
// write-path behaviour. Doing it on read would suppress stored data.
func TestStampDERInstance_DropsInjectedLinksOnPUTOnly(t *testing.T) {
	t.Parallel()

	stamp := coredel.StampDERInstance(allMounted)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			d := sep2.DER{AssociatedDERProgramListLink: &sep2.ListLink{Href: testBase + "/derp"}}
			stamp(httptest.NewRequest(method, testBase, nil), &d)

			dropped := d.AssociatedDERProgramListLink == nil
			if wantDropped := method == http.MethodPut; dropped != wantDropped {
				t.Errorf("AssociatedDERProgramListLink dropped = %v, want %v", dropped, wantDropped)
			}
		})
	}
}

// TestDERListBuilder_DerivesPerMemberFromItsOwnHref proves the list and the
// instance route agree, and that a multi-DER list gives each member its own
// sub-resource hrefs rather than collapsing onto one DER.
func TestDERListBuilder_DerivesPerMemberFromItsOwnHref(t *testing.T) {
	t.Parallel()

	result := storeResult(
		sep2.DER{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/7/der/1"}}},
		sep2.DER{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/7/der/2"}}},
	)

	list := coredel.DERListBuilder(allMounted)("/edev/7/der", result, 900)
	if len(list.DER) != 2 {
		t.Fatalf("len(DER) = %d, want 2", len(list.DER))
	}
	for i, want := range []string{"/edev/7/der/1", "/edev/7/der/2"} {
		assertLinks(t, list.DER[i], map[string]string{
			"DERAvailabilityLink": want + "/dera",
			"DERCapabilityLink":   want + "/dercap",
			"DERSettingsLink":     want + "/derg",
			"DERStatusLink":       want + "/ders",
		})
	}
}

// assertLinks checks the named link fields against want, where an empty want
// means the field must be absent. Absence is asserted rather than ignored:
// under section 4.4 an absent link is a positive statement that a function set
// is not implemented, so a spurious one is a defect in its own right.
func assertLinks(t *testing.T, d sep2.DER, want map[string]string) {
	t.Helper()

	got := map[string]*sep2.Link{
		"DERAvailabilityLink": d.DERAvailabilityLink,
		"DERCapabilityLink":   d.DERCapabilityLink,
		"DERSettingsLink":     d.DERSettingsLink,
		"DERStatusLink":       d.DERStatusLink,
	}

	for field, wantHref := range want {
		if field == "AssociatedDERProgramListLink" {
			switch {
			case wantHref == "" && d.AssociatedDERProgramListLink != nil:
				t.Errorf("%s = %q, want absent", field, d.AssociatedDERProgramListLink.Href)
			case wantHref != "" && (d.AssociatedDERProgramListLink == nil || d.AssociatedDERProgramListLink.Href != wantHref):
				t.Errorf("%s = %+v, want %q", field, d.AssociatedDERProgramListLink, wantHref)
			}
			continue
		}

		link, known := got[field]
		if !known {
			t.Fatalf("test names an unknown link field %q", field)
		}
		switch {
		case wantHref == "" && link != nil:
			t.Errorf("%s = %q, want absent", field, link.Href)
		case wantHref != "" && link == nil:
			t.Errorf("%s is absent, want %q", field, wantHref)
		case wantHref != "" && link.Href != wantHref:
			t.Errorf("%s.Href = %q, want %q", field, link.Href, wantHref)
		}
	}
}
