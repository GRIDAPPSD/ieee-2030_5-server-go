package bootfixture

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
)

const (
	kindEndDevice  = "EndDevice"
	kindDERProgram = "DERProgram"

	seedRecordVersion = 1
)

// seedKey names one persisted-kind fixture record. Parent is empty for an
// EndDevice.
type seedKey struct {
	Kind   string `json:"kind"`
	Parent string `json:"parent"`
	ID     string `json:"id"`
}

type seedRecord struct {
	Version int       `json:"version"`
	Records []seedKey `json:"records"`
}

// readSeedRecord returns the seeded set. A missing file is an empty set. Any
// other content it cannot trust fails: an unreadable or empty file, an unknown
// version, no records, or a key whose shape does not fit its kind. The writer
// produces none of these, and reading one as an empty or partial set would
// recreate records deleted since they were seeded.
func readSeedRecord(path string) (map[seedKey]struct{}, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[seedKey]struct{}), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read seed record %s: %w", path, err)
	}
	var rec seedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("decode seed record %s: %w", path, err)
	}
	if rec.Version != seedRecordVersion {
		return nil, fmt.Errorf("seed record %s: unsupported version %d (want %d)", path, rec.Version, seedRecordVersion)
	}
	// Absent, null and [] all decode to an empty slice. The seed record is
	// written only after a key is added, so it always holds one.
	if len(rec.Records) == 0 {
		return nil, fmt.Errorf("seed record %s: no records", path)
	}
	seeded := make(map[seedKey]struct{}, len(rec.Records))
	for i, k := range rec.Records {
		if !k.wellFormed() {
			return nil, fmt.Errorf("seed record %s: records[%d]: invalid key kind=%q parent=%q id=%q", path, i, k.Kind, k.Parent, k.ID)
		}
		seeded[k] = struct{}{}
	}
	return seeded, nil
}

// wellFormed reports whether the key has the shape its kind is written with:
// an EndDevice has no parent and a DERProgram always has one.
func (k seedKey) wellFormed() bool {
	if k.ID == "" {
		return false
	}
	switch k.Kind {
	case kindEndDevice:
		return k.Parent == ""
	case kindDERProgram:
		return k.Parent != ""
	default:
		return false
	}
}

// writeSeedRecord replaces the seed record through a synced temporary file
// and a rename, so a crash leaves either the old set or the new one.
func writeSeedRecord(path string, seeded map[seedKey]struct{}) error {
	keys := make([]seedKey, 0, len(seeded))
	for k := range seeded {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b seedKey) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Parent, b.Parent), cmp.Compare(a.ID, b.ID))
	})
	payload, err := json.Marshal(seedRecord{Version: seedRecordVersion, Records: keys})
	if err != nil {
		return fmt.Errorf("encode seed record: %w", err)
	}

	if err := atomicfile.Write(path, payload); err != nil {
		return fmt.Errorf("write seed record %s: %w", path, err)
	}
	return nil
}
