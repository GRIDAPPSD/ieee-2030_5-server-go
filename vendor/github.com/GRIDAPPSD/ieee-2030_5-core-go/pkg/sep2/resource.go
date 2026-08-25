package sep2

const (
	// Namespace is the IEEE 2030.5-2018 XML namespace (also used by 2023).
	Namespace = "urn:ieee:std:2030.5:ns"

	// Namespace2013 is the IEEE 2030.5-2013 XML namespace
	// used by older clients (e.g., EPRI reference client).
	Namespace2013 = "http://ieee.org/2030.5"

	// Namespace2023 is the IEEE 2030.5-2023 XML namespace.
	// Same as 2018 — the namespace didn't change in the 2023 revision.
	Namespace2023 = Namespace
)

// Resource is the base type for all IEEE 2030.5 resources.
// All resources have an optional href attribute identifying their URI.
type Resource struct {
	Href string `xml:"href,attr,omitempty"`
}

// Link provides a typed reference to another resource.
type Link struct {
	Href string `xml:"href,attr"`
}

// ListLink provides a reference to a list resource with a count of items.
type ListLink struct {
	Href string `xml:"href,attr"`
	All  uint32 `xml:"all,attr,omitempty"`
}

// SubscribableResource extends Resource with subscription support.
type SubscribableResource struct {
	Resource
	Subscribable *uint8 `xml:"subscribable,attr,omitempty"`
}

// ListResource is the base type for all list resources.
// It supports paging via the all/results attributes and
// the s (start), l (limit), a (after) query parameters.
type ListResource struct {
	SubscribableResource
	All      uint32 `xml:"all,attr"`
	Results  uint32 `xml:"results,attr"`
	PollRate uint32 `xml:"pollRate,attr,omitempty"`
}
