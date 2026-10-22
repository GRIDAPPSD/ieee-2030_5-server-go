package server

// SetStartDeviceLog replaces the function Run uses to start the device log
// writer and returns the previous one, so an external test can capture the
// writer and check Run closed it.
func SetStartDeviceLog(f DeviceLogStarter) DeviceLogStarter {
	prev := startDeviceLog
	startDeviceLog = f
	return prev
}
