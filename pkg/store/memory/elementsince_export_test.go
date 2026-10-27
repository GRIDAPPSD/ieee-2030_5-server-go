package memory

import "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"

func DERStatusElementsForTest(s sep2.DERStatus) map[string]string { return derStatusElements(s) }

func DERAvailabilityElementsForTest(a sep2.DERAvailability) map[string]string {
	return derAvailabilityElements(a)
}
