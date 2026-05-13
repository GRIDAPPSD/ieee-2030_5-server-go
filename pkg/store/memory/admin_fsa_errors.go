package memory

import "errors"

// ErrAdminFSAInUse is returned when an admin FSA cannot be deleted because
// it still has programs attached or devices assigned. The caller must
// detach/unlink first — the store never silently cascades.
var ErrAdminFSAInUse = errors.New("admin FSA still has programs or devices attached")
