package memory

// Test-only clock hook for RegisteredEndDeviceStore.
//
// Registration.dateTimeRegistered is minOccurs=1 in sep.xsd, so the binding
// has to stamp a real time. A test that can only assert "not zero" would
// pass on a value the encoder invented as easily as on the one the store
// wrote, which is precisely the assertion strength [[data-invariants]] Rule
// 1 exists to rule out. Pinning the clock turns that into an equality check
// on the served bytes.
//
// It is an exported function rather than an _export_test.go helper because
// the tests that need it live in pkg/sep2srv/assembly, a different package,
// where an _export_test.go hook would be invisible. The "ForTest" suffix is
// the project's existing signal (see subscription_testhooks.go) that a
// symbol MUST NOT be called from production code.

// SetRegistrationClockForTest replaces the clock a binding stamps
// dateTimeRegistered from. A nil now restores the real clock.
//
// TEST-ONLY. Call it before the store serves any request: it is not
// synchronized against concurrent Create calls, because a production path
// never touches it.
func SetRegistrationClockForTest(s *RegisteredEndDeviceStore, now func() int64) {
	s.now = now
}
