package store

import "time"

// ElementSinceReader reports, per element of a stored resource, when the
// element's current value was first seen. Element names are the XML element
// names of the resource (for example "stateOfChargeStatus"); an element the
// resource does not carry has no entry, so absence is never read as a time.
type ElementSinceReader interface {
	// ElementSince returns an independent copy of the times recorded for the
	// resource stored under (parentID, id). It is empty when nothing is
	// recorded, including after the resource was deleted.
	ElementSince(parentID, id string) map[string]time.Time
}
