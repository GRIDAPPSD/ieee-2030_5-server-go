package obs

// See metrics_test.go's package note: these assert on the DELTA of a counter
// around the operation under test, never on an absolute value, since the
// collectors persist across this package's tests.

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestRecordAdminAuthFailureLabelsByAdmissionPath asserts the counter is
// labeled by the admission path passed in (#413).
func TestRecordAdminAuthFailureLabelsByAdmissionPath(t *testing.T) {
	before := testutil.ToFloat64(adminAuthFailures.WithLabelValues(AdminAdmissionPathBearer))
	RecordAdminAuthFailure(AdminAdmissionPathBearer)
	after := testutil.ToFloat64(adminAuthFailures.WithLabelValues(AdminAdmissionPathBearer))

	if after-before != 1 {
		t.Errorf("bearer admission_path delta = %v, want 1", after-before)
	}
}

// TestRecordAdminAuthFailureBoundsUnknownAdmissionPath asserts an admission
// path outside the known set folds to AdminAdmissionPathOther rather than
// minting a new series, mirroring the notifications cardinality guard.
func TestRecordAdminAuthFailureBoundsUnknownAdmissionPath(t *testing.T) {
	const bogus = "totally-made-up-admission-path"

	otherBefore := testutil.ToFloat64(adminAuthFailures.WithLabelValues(AdminAdmissionPathOther))
	RecordAdminAuthFailure(bogus)
	otherAfter := testutil.ToFloat64(adminAuthFailures.WithLabelValues(AdminAdmissionPathOther))

	if otherAfter-otherBefore != 1 {
		t.Errorf("AdminAdmissionPathOther delta = %v, want 1 (unknown admission path must fold to other)",
			otherAfter-otherBefore)
	}
	if got := testutil.ToFloat64(adminAuthFailures.WithLabelValues(bogus)); got != 0 {
		t.Errorf("bogus admission path %q minted a series with value %v, want 0", bogus, got)
	}
}
