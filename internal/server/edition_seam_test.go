package server

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// #798: the protocol assembly learns the edition from the server Stores.
func TestNewCoreStores_CarriesEdition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		edition handler.SEP2Edition
		want    bool
	}{
		{"", false},
		{handler.Edition2018, false},
		{handler.Edition2023, true},
	} {
		if got := NewCoreStores(&Stores{Sep2Edition: tc.edition}).Edition2023; got != tc.want {
			t.Errorf("edition %q: Edition2023 = %v, want %v", tc.edition, got, tc.want)
		}
	}
}
