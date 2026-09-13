package assembly_test

import (
	"reflect"
	"testing"
)

// TestTestStoresWiresEveryStoresField fails when a Stores field is added and
// testStores neither sets it nor states why it is left unset.
func TestTestStoresWiresEveryStoresField(t *testing.T) {
	t.Parallel()
	unset := map[string]string{
		"EndDeviceIndexes": "nil selects the process-local index these tests rely on",
		"AdminFSAs":        "the admin plane is not mounted on the protocol router",
	}
	v := reflect.ValueOf(testStores()).Elem()
	seen := map[string]bool{}
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		seen[name] = true
		reason, allowed := unset[name]
		switch zero := v.Field(i).IsZero(); {
		case zero && !allowed:
			t.Errorf("testStores leaves Stores.%s unset; set it or state why not", name)
		case !zero && allowed:
			t.Errorf("testStores sets Stores.%s, which is listed as unset (%s)", name, reason)
		}
	}
	for name := range unset {
		if !seen[name] {
			t.Errorf("the unset list names Stores.%s, which does not exist", name)
		}
	}
}
