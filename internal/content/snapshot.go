package content

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// checkSnapshot refuses a snapshot the store would load as something other than
// what an operator saved: one with no "records" key at all, a record under a
// parent the flat view cannot reach, and a record whose value the family's own
// Validate refuses (a value of {} decodes to the zero type and would otherwise
// bypass it). The store has already refused a bad version, an empty id and a
// record that does not decode. A "records" of null is accepted: the store
// writes it when the last record is deleted.
func checkSnapshot[T store.Copier[T]](path string, spec Spec[T]) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(data) == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read snapshot %q: %w", path, err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("decode snapshot %q: %w", path, err)
	}
	raw, ok := env["records"]
	if !ok {
		return fmt.Errorf("snapshot %q has no records key", path)
	}
	var recs []struct {
		Parent string          `json:"parent"`
		ID     string          `json:"id"`
		Value  json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &recs); err != nil {
		return fmt.Errorf("decode snapshot records in %q: %w", path, err)
	}
	for i, r := range recs {
		if r.Parent != memory.FlatParent {
			return fmt.Errorf("snapshot record %d (%s) is under parent %q, not %q, so nothing could read it", i, shorten(r.ID), shorten(r.Parent), memory.FlatParent)
		}
		var v T
		if err := json.Unmarshal(r.Value, &v); err != nil {
			return fmt.Errorf("snapshot record %d (%s): %w", i, shorten(r.ID), err)
		}
		if spec.Validate != nil {
			if err := spec.Validate(v); err != nil {
				return fmt.Errorf("snapshot record %d (%s) fails validation: %w", i, shorten(r.ID), err)
			}
		}
		if err := checkHref(spec, v, r.ID); err != nil {
			return fmt.Errorf("snapshot record %d (%s): %w", i, shorten(r.ID), err)
		}
	}
	return nil
}
