package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
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
