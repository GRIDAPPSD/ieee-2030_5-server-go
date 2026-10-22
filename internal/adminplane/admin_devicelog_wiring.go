package adminplane

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

// newAdminDeviceLogHandler builds the #1066 device log handler. A nil reader
// is device logging off, which the handler answers with 503. Managers is
// read through the narrow reader interface, so a store that is not the
// in-memory one still supplies manager lines; a nil store leaves them out.
func newAdminDeviceLogHandler(reader *devicelog.Reader, stores *Stores) *handler.AdminDeviceLogHandler {
	h := &handler.AdminDeviceLogHandler{Reader: reader}
	if stores != nil && stores.EndDeviceManagers != nil {
		h.Managers = stores.EndDeviceManagers
	}
	return h
}
