package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol/dercurve"
)

// Shared persistence machinery for admin-mutated stores
// (GRIDAPPSD/ieee-2030_5-server-go#165).
//
// Every persistent store wrapper writes a single JSON document of the shape
//
//	{"version": 1, "records": [...]}
//
// to a configured path. Writes go through atomicfile.Write: a synced
// <path>.tmp renamed onto <path>, so a crash leaves the previously committed
// <path> intact.
//
// The on-disk version number is the contract between writers and readers.
// Bump persistenceVersion on a schema-breaking change; readers reject
// unknown versions rather than silently re-deriving state from a future
// snapshot they don't understand.
//
// Concurrency: each wrapper holds its own persistMu so writers serialize
// against each other but readers stay non-blocked. The underlying memory
// Store's RWMutex keeps the in-memory state consistent; the snapshot is
// taken under that read lock and written outside it so the file syscall
// doesn't stall HTTP handlers.

// PersistenceVersion is the current on-disk schema version. Bumped on a
// schema-breaking change.
const PersistenceVersion = 1

// snapshotEnvelope is the root JSON document written by every store
// wrapper. Records is a json.RawMessage so each store can carry its own
// record shape without dragging a shared union type into pkg/store/memory.
type snapshotEnvelope struct {
	Version int             `json:"version"`
	Records json.RawMessage `json:"records"`
}

// readSnapshotEnvelope loads a snapshot envelope from disk. A missing file
// is treated as cold boot: returns (nil, nil). An empty file is treated
// as "no records": returns an envelope with empty records. A corrupt file
// returns a descriptive error and leaves the caller to decide.
//
// readSnapshotEnvelope validates the version and returns the raw records
// bytes so the caller can json.Unmarshal them into its own slice type.
func readSnapshotEnvelope(path string) (*snapshotEnvelope, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	if len(data) == 0 {
		// Empty file is a valid "no records" state (e.g. previous write
		// landed with an empty record set).
		return &snapshotEnvelope{Version: PersistenceVersion}, nil
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode %q: %w", path, err)
	}
	if env.Version != PersistenceVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d at %q (want %d)",
			env.Version, path, PersistenceVersion)
	}
	return &env, nil
}

// writeSnapshotEnvelope marshals records as JSON and writes the snapshot
// envelope atomically to path. The records argument is anything
// json.Marshal can encode (typically a slice). A wrapper-level mutex must
// be held by the caller to serialize concurrent writes.
func writeSnapshotEnvelope(path string, records any) error {
	if path == "" {
		return nil
	}
	recordsBytes, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("marshal records: %w", err)
	}
	env := snapshotEnvelope{
		Version: PersistenceVersion,
		Records: recordsBytes,
	}
	payload, err := json.Marshal(&env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	// Each caller already wraps this with its own store label
	// (e.g. "derprogram persistence: %w"); adding one here would
	// double-label every caller but the one it was copied from.
	return atomicfile.Write(path, payload)
}

// decodeSnapshotRecords decodes a snapshot's records one at a time, so a
// record that does not decode is refused by its position and id instead of
// failing the whole file anonymously. The snapshot stores in this package
// that keep a {"version", "records"} envelope load through it.
func decodeSnapshotRecords[R any](raw json.RawMessage) ([]R, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, fmt.Errorf("decode records: %w", err)
	}
	records := make([]R, 0, len(elems))
	for i, elem := range elems {
		var r R
		if err := json.Unmarshal(elem, &r); err != nil {
			if field, shape := preV026DERControlBaseField(elem); field != "" {
				err = fmt.Errorf("%s is in the DERControlBase shape written before core v0.26.0, which this server cannot read or convert; "+
					"rewrite it as %s or remove the record, and check the later records too: %w", field, shape, err)
			}
			return nil, fmt.Errorf("decode record %d%s: %w", i, recordName(elem), err)
		}
		records = append(records, r)
	}
	return records, nil
}

// recordName is " (parent/id)" or " (id)" for a record carrying those keys,
// else "". It only labels an error, so a record it cannot read gets no label.
func recordName(elem json.RawMessage) string {
	var key struct {
		Parent string `json:"parent"`
		ID     string `json:"id"`
	}
	if json.Unmarshal(elem, &key) != nil || key.ID == "" {
		return ""
	}
	if key.Parent != "" {
		return fmt.Sprintf(" (%s/%s)", key.Parent, key.ID)
	}
	return fmt.Sprintf(" (%s)", key.ID)
}

// The current shapes of the modes core v0.26.0 changed, as the refusal
// states them.
const (
	curveShape     = `{"Href":"/dc/<curve id>"}`
	fixedVarShape  = `{"RefType":1|2|3,"Value":<hundredths of a percent of the device rating RefType names>}`
	freqDroopShape = `{"DBOF":n,"DBUF":n,"KOF":n,"KUF":n,"OpenLoopTms":n}`
)

// preV026DERControlBaseField returns the path of the first DERControlBase
// mode in elem stored in the core v0.25.0 shape (a curve link as a number,
// OpModFixedVar as a ReactivePower with Multiplier and Value, or
// OpModFreqDroop as a number) and that mode's current shape. "" when there
// is none.
func preV026DERControlBaseField(elem json.RawMessage) (field, shape string) {
	dec := json.NewDecoder(bytes.NewReader(elem))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return "", ""
	}
	return findPreV026Field(v, "", dercurve.LinkFields())
}

func findPreV026Field(v any, path string, curveFields []string) (field, shape string) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			child := strings.TrimPrefix(path+"."+k, ".")
			_, isNumber := t[k].(json.Number)
			obj, isObject := t[k].(map[string]any)
			switch {
			case isNumber && slices.Contains(curveFields, k):
				return child, curveShape
			case isNumber && k == "OpModFreqDroop":
				return child, freqDroopShape
			case isObject && k == "OpModFixedVar":
				if _, old := obj["Multiplier"]; old {
					return child, fixedVarShape
				}
			}
			if field, shape := findPreV026Field(t[k], child, curveFields); field != "" {
				return field, shape
			}
		}
	case []any:
		for i, e := range t {
			if field, shape := findPreV026Field(e, fmt.Sprintf("%s[%d]", path, i), curveFields); field != "" {
				return field, shape
			}
		}
	}
	return "", ""
}
