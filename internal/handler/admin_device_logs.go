package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2capture"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #1066: the read API for a device's log.
//
//	GET /api/device-logs/{lfdi}            one page of lines, newest first
//	GET /api/device-logs/{lfdi}/download   the device's own two files, oldest first
//
// Both sit under a sensitive read prefix (admin_sensitive_routes.go): the
// log shows each device's activity and its manager's actions.

const (
	// maxDeviceLogKinds and maxManagedLogSources bound the work one request
	// can ask for: each source opens up to two files.
	maxDeviceLogKinds    = 8
	maxManagedLogSources = 200

	// deviceLogReadBudget is how long one read may run, under the admin
	// listener's 30 s write timeout.
	deviceLogReadBudget = 25 * time.Second
)

var deviceLogKindRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// AdminDeviceLogHandler serves a device's log from the files the device log
// Writer keeps.
type AdminDeviceLogHandler struct {
	// Reader reads the log directory. Nil means device logging is off, and
	// both routes answer 503.
	Reader *devicelog.Reader
	// Managers names a device's manager and an aggregator's managed devices.
	// Nil leaves the manager's lines and scope=managed out.
	Managers store.EndDeviceManagementReader
}

// deviceLogResponse is the body of GET /api/device-logs/{lfdi}. Entries is
// never null; the cursors are null when there is no such page.
type deviceLogResponse struct {
	LFDI        string           `json:"lfdi"`
	Entries     []devicelog.Line `json:"entries"`
	NextBefore  *string          `json:"nextBefore"`
	NextAfter   *string          `json:"nextAfter"`
	RotatedRead bool             `json:"rotatedRead"`
	Dropped     uint64           `json:"dropped"`
}

// HandleRead returns the handler for GET /api/device-logs/{lfdi}.
//
// Query: limit (1 to 1000, default 200), before or after (a cursor from an
// earlier page, not both), kind (comma list), scope (own, the default, or
// managed). A device's own view is its file, its lines in the shared
// unregistered file, and its manager's lines that name it. scope=managed on
// an aggregator adds each managed device's file and shared-file lines.
func (h *AdminDeviceLogHandler) HandleRead() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Reader == nil {
			writeError(w, http.StatusServiceUnavailable, "device log disabled")
			return
		}
		lfdi, err := normalizeLFDI(r.PathValue("lfdi"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "lfdi: "+err.Error())
			return
		}
		q, msg := parseDeviceLogQuery(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		sources, status, msg := h.sources(r.Context(), lfdi, q.managed)
		if status != 0 {
			writeError(w, status, msg)
			return
		}
		q.Query.Sources = sources

		// A read that outlives its client or the server's write timeout
		// (30 s) stops here instead of running on.
		ctx, cancel := context.WithTimeout(r.Context(), deviceLogReadBudget)
		defer cancel()
		page, err := h.Reader.Read(ctx, q.Query)
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusServiceUnavailable, "device log read did not finish")
			return
		case errors.Is(err, devicelog.ErrBadQuery):
			writeError(w, http.StatusBadRequest, "invalid device log query")
			return
		case errors.Is(err, devicelog.ErrNotFound):
			writeError(w, http.StatusNotFound, "no log for this device")
			return
		case errors.Is(err, devicelog.ErrBadCursor):
			writeError(w, http.StatusBadRequest, "before and after must be cursors from an earlier page")
			return
		case err != nil:
			log.Printf("admin: device log read for %s: %s", logsafe.Str(lfdi), logsafe.Str(err.Error()))
			writeError(w, http.StatusInternalServerError, "device log unreadable")
			return
		}
		resp := deviceLogResponse{
			LFDI:        lfdi,
			Entries:     page.Entries,
			NextBefore:  optionalString(page.NextBefore),
			NextAfter:   optionalString(page.NextAfter),
			RotatedRead: page.RotatedRead,
			Dropped:     page.Dropped,
		}
		if resp.Entries == nil {
			resp.Entries = []devicelog.Line{}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// HandleDownload returns the handler for GET /api/device-logs/{lfdi}/download:
// the device's own rotated and current files, concatenated oldest first. The
// shared and manager files are not merged in.
func (h *AdminDeviceLogHandler) HandleDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Reader == nil {
			writeError(w, http.StatusServiceUnavailable, "device log disabled")
			return
		}
		lfdi, err := normalizeLFDI(r.PathValue("lfdi"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "lfdi: "+err.Error())
			return
		}
		d, err := h.Reader.OpenDownload(lfdi)
		switch {
		case errors.Is(err, devicelog.ErrNotFound):
			writeError(w, http.StatusNotFound, "no log for this device")
			return
		case err != nil:
			log.Printf("admin: device log download for %s: %s", logsafe.Str(lfdi), logsafe.Str(err.Error()))
			writeError(w, http.StatusInternalServerError, "device log unreadable")
			return
		}
		defer func() { _ = d.Close() }()
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.jsonl"`, lfdi))
		w.WriteHeader(http.StatusOK)
		if _, err := d.WriteTo(w); err != nil {
			log.Printf("admin: device log download for %s cut short: %s", logsafe.Str(lfdi), logsafe.Str(err.Error()))
		}
	}
}

type deviceLogRequest struct {
	devicelog.Query
	managed bool
}

func parseDeviceLogQuery(r *http.Request) (deviceLogRequest, string) {
	v := r.URL.Query()
	q := deviceLogRequest{Query: devicelog.Query{Before: v.Get("before"), After: v.Get("after")}}
	limit, err := sep2capture.ParsePageLimit(v.Get("limit"), sep2capture.RefuseOverMax)
	if err != nil {
		return q, fmt.Sprintf("limit must be a whole number from 1 to %d", sep2capture.MaxPageLimit)
	}
	q.Limit = limit
	if q.Before != "" && q.After != "" {
		return q, "before and after are mutually exclusive"
	}
	if raw := v.Get("kind"); raw != "" {
		kinds := strings.Split(raw, ",")
		if len(kinds) > maxDeviceLogKinds {
			return q, fmt.Sprintf("kind lists at most %d kinds", maxDeviceLogKinds)
		}
		for _, k := range kinds {
			if !deviceLogKindRE.MatchString(k) {
				return q, "kind must be a comma list of lower-case names"
			}
		}
		q.Kinds = kinds
	}
	switch v.Get("scope") {
	case "", "own":
	case "managed":
		q.managed = true
	default:
		return q, "scope must be own or managed"
	}
	return q, ""
}

// sources builds the streams of lfdi's view. A non-zero status is the
// refusal to send.
func (h *AdminDeviceLogHandler) sources(ctx context.Context, lfdi string, managed bool) ([]devicelog.Source, int, string) {
	actors := map[string]bool{lfdi: true}
	srcs := []devicelog.Source{{File: lfdi}}
	in := map[string]bool{lfdi: true}

	if h.Managers != nil {
		manager, err := h.Managers.ManagerOf(ctx, lfdi)
		switch {
		case err == nil:
			if !sep2lfdi.IsCanonical(manager) {
				return nil, http.StatusBadRequest, "the device's manager is not a canonical LFDI"
			}
			if !in[manager] {
				in[manager] = true
				srcs = append(srcs, devicelog.Source{File: manager, Target: lfdi, Label: "manager"})
			}
		case !errors.Is(err, store.ErrNotFound):
			log.Printf("admin: device log manager lookup for %s: %s", logsafe.Str(lfdi), logsafe.Str(err.Error()))
			return nil, http.StatusInternalServerError, "device log unreadable"
		}
		if managed {
			devices, err := h.Managers.ManagedBy(ctx, lfdi)
			if err != nil {
				log.Printf("admin: device log managed lookup for %s: %s", logsafe.Str(lfdi), logsafe.Str(err.Error()))
				return nil, http.StatusInternalServerError, "device log unreadable"
			}
			if len(devices) > maxManagedLogSources {
				return nil, http.StatusBadRequest, fmt.Sprintf("scope=managed covers at most %d managed devices", maxManagedLogSources)
			}
			for _, d := range devices {
				if !sep2lfdi.IsCanonical(d) {
					return nil, http.StatusBadRequest, "a managed device is not a canonical LFDI"
				}
				actors[d] = true
				if !in[d] {
					in[d] = true
					srcs = append(srcs, devicelog.Source{File: d, Label: "managed"})
				}
			}
		}
	}
	return append(srcs, devicelog.Source{File: devicelog.UnregisteredFile, Actors: actors, Label: "unregistered"}), 0, ""
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
