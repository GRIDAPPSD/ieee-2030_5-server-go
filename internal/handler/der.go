package handler

import (
	"fmt"
	"net/http"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// BuildDERList constructs a DERList from store results.
func BuildDERList(href string, result store.ListResult[sep2.DER], pollRate uint32) sep2.DERList {
	return sep2.DERList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DER: result.Items,
	}
}

// BuildDERProgramList constructs a DERProgramList from store results.
func BuildDERProgramList(href string, result store.ListResult[sep2.DERProgram], pollRate uint32) sep2.DERProgramList {
	return sep2.DERProgramList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DERProgram: result.Items,
	}
}

// BuildDERControlList constructs a DERControlList from store results.
func BuildDERControlList(href string, result store.ListResult[sep2.DERControl], pollRate uint32) sep2.DERControlList {
	return sep2.DERControlList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DERControl: result.Items,
	}
}

// BuildDERCurveList constructs a DERCurveList from store results.
func BuildDERCurveList(href string, result store.ListResult[sep2.DERCurve], pollRate uint32) sep2.DERCurveList {
	return sep2.DERCurveList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DERCurve: result.Items,
	}
}

// DERSingletonHandlers creates all DER singleton GET/PUT handlers
// for DERCapability, DERSettings, DERStatus, DERAvailability.
func DERSingletonHandlers(
	caps *memory.ScopedStore[sep2.DERCapability],
	settings *memory.ScopedStore[sep2.DERSettings],
	statuses *memory.ScopedStore[sep2.DERStatus],
	avails *memory.ScopedStore[sep2.DERAvailability],
) (dercap, derg, ders, dera http.HandlerFunc) {

	derParentKey := func(r *http.Request) string {
		return r.PathValue("id") + "/" + r.PathValue("derId")
	}

	dercap = HandleSingletonGetPut[sep2.DERCapability](caps, derParentKey,
		func(r *http.Request) sep2.DERCapability {
			return sep2.DERCapability{Resource: sep2.Resource{
				Href: fmt.Sprintf("/edev/%s/der/%s/dercap", r.PathValue("id"), r.PathValue("derId")),
			}}
		})

	derg = HandleSingletonGetPut[sep2.DERSettings](settings, derParentKey,
		func(r *http.Request) sep2.DERSettings {
			s := sep2.DERSettings{}
			s.Href = fmt.Sprintf("/edev/%s/der/%s/derg", r.PathValue("id"), r.PathValue("derId"))
			return s
		})

	ders = HandleSingletonGetPut[sep2.DERStatus](statuses, derParentKey,
		func(r *http.Request) sep2.DERStatus {
			s := sep2.DERStatus{}
			s.Href = fmt.Sprintf("/edev/%s/der/%s/ders", r.PathValue("id"), r.PathValue("derId"))
			return s
		})

	dera = HandleSingletonGetPut[sep2.DERAvailability](avails, derParentKey,
		func(r *http.Request) sep2.DERAvailability {
			s := sep2.DERAvailability{}
			s.Href = fmt.Sprintf("/edev/%s/der/%s/dera", r.PathValue("id"), r.PathValue("derId"))
			return s
		})

	return
}

// DefaultDERControlHandler creates a handler for GET/PUT on DefaultDERControl.
func DefaultDERControlHandler(store *memory.ScopedStore[sep2.DefaultDERControl]) http.HandlerFunc {
	return HandleSingletonGetPut[sep2.DefaultDERControl](store,
		func(r *http.Request) string {
			return r.PathValue("id") + "/" + r.PathValue("fsaId") + "/" + r.PathValue("derpId")
		},
		func(r *http.Request) sep2.DefaultDERControl {
			s := sep2.DefaultDERControl{}
			s.Href = fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/dderc",
				r.PathValue("id"), r.PathValue("fsaId"), r.PathValue("derpId"))
			return s
		})
}
