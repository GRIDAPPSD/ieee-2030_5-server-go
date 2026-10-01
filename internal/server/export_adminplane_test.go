package server

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"

// The admin plane moved to internal/adminplane. These names keep the tests in
// this package that also use the server's own store wiring reading as before.
var (
	newAdminFlowReservationHandler = adminplane.NewAdminFlowReservationHandler
	newAdminDERControlHandler      = adminplane.NewAdminDERControlHandler
	recoveryWriters                = adminplane.RecoveryWriters
	persists                       = adminplane.Persists
)
