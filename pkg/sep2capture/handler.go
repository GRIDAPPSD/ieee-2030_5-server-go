package sep2capture

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Handler returns the GET-only HTTP handler the admin UI's Traffic tab
// calls (PR 4 of #611). It carries no auth of its own: the caller mounts
// it behind whatever admin authentication already guards their mux. Every
// route below answers only GET; any other method on a registered pattern
// gets net/http's own 405 with an Allow header, since every pattern is
// registered with a "GET " method prefix.
//
// # Routes
//
// GET /clients
//
//	Every client this Store has ever seen, oldest key first.
//	200 application/json: {"clients": [{"key": "...", "firstSeen":
//	"2026-09-22T10:00:00Z", "lastSeen": "2026-09-22T10:05:00Z",
//	"exchangeCount": 3}]}
//
// GET /exchanges?client=<key>&after=<id>&limit=<n>
//
//	One client's exchange summaries, oldest first. client is required.
//	after (default 0) returns only ids greater than it. limit (default
//	200, clamped to at most 1000) bounds how many summaries come back; a
//	limit of 0 or less is a 400, not "unbounded" (Store.Exchanges itself
//	allows unbounded; this route never does).
//	200 application/json: {"exchanges": [{"id": 7, "connId": 2,
//	"clientKey": "3e4f...", "started": "2026-09-22T10:00:00Z",
//	"ended": "2026-09-22T10:00:01Z", "mark": "handled", "handlerRuns": 1,
//	"method": "GET", "path": "/dcap", "status": 200, "reqTrueLen": 128,
//	"respTrueLen": 512, "reqStored": 128, "respStored": 512,
//	"reqTruncated": false, "respTruncated": false}]}
//	400 when client is missing, after is not a uint64, or limit is not a
//	positive integer.
//
// GET /exchanges/{id}
//
//	One exchange's summary, the same shape as one entry above.
//	200 application/json: {"id": 7, "connId": 2, "clientKey": "3e4f...",
//	"started": "2026-09-22T10:00:00Z", "ended": "2026-09-22T10:00:01Z",
//	"mark": "handled", "handlerRuns": 1, "method": "GET", "path": "/dcap",
//	"status": 200, "reqTrueLen": 128, "respTrueLen": 512, "reqStored": 128,
//	"respStored": 512, "reqTruncated": false, "respTruncated": false}
//	400 when id does not parse as a uint64.
//	404 when id is higher than any exchange this Store has ever indexed.
//	410 when id was indexed but its segment has since been evicted.
//
// GET /exchanges/{id}/request
// GET /exchanges/{id}/response
//
//	The exact bytes captured for one direction of one exchange, exactly
//	as they crossed the wire (truncated past 4 MiB, per the stored
//	summary's reqTruncated/respTruncated). 200
//	application/octet-stream, Content-Disposition: attachment, and
//	X-Content-Type-Options: nosniff. Same 400/404/410 as above.
//
// GET /stream?after=<id>
//
//	Server-Sent Events: one "data:" line of the JSON exchange-summary
//	shape above per newly recorded exchange, with "id:" set to the
//	exchange id. A reconnect sends either the standard Last-Event-ID
//	header or this route's own after= query parameter (Last-Event-ID
//	wins when both are present) to resume from the last id it saw,
//	getting exactly what it missed with no gap and no duplicate, as long
//	as those ids are still indexed (an evicted id is silently skipped,
//	the same way an evicted exchange never appears in /exchanges).
//	Neither present means live only, starting from this connection: the
//	route never replays history on its own, so opening the tab for the
//	first time does not have to hold the whole index in memory. after=0
//	is a valid, explicit way to ask for every already-indexed exchange.
//	A reader that falls behind is dropped (Stats.SlowSubscribers) rather
//	than allowed to slow capture for anyone else.
//
// GET /stats
//
//	The drop and loss counters the tab shows.
//	200 application/json: {"droppedQueueFull": 0, "droppedWriteError": 0,
//	"abandoned": 0, "truncated": 0, "evictedSegments": 0,
//	"indexMemoryEvictions": 0, "slowSubscribers": 0, "bytesOnDisk": 4096,
//	"indexEntries": 3, "indexApproxBytes": 900, "duplicateIndexIds": 0}
//
// Field names are stable and lower camelCase; PR 6 (the Traffic tab) is
// the contract's first consumer.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /clients", s.handleClients)
	mux.HandleFunc("GET /exchanges", s.handleExchangeList)
	mux.HandleFunc("GET /exchanges/{id}", s.handleExchangeSummary)
	mux.HandleFunc("GET /exchanges/{id}/request", s.handleExchangeDirection(directionRequest))
	mux.HandleFunc("GET /exchanges/{id}/response", s.handleExchangeDirection(directionResponse))
	mux.HandleFunc("GET /stream", s.handleStream)
	mux.HandleFunc("GET /stats", s.handleStats)
	return mux
}

// defaultExchangesLimit and maxExchangesLimit are this route's own bound
// on /exchanges (Q7 item 4: "bounded"), independent of Store.Exchanges,
// which treats a non-positive limit as unbounded.
const (
	defaultExchangesLimit = 200
	maxExchangesLimit     = 1000
)

// clientJSON is the wire shape of one Clients() entry.
type clientJSON struct {
	Key           string `json:"key"`
	FirstSeen     string `json:"firstSeen"`
	LastSeen      string `json:"lastSeen"`
	ExchangeCount uint64 `json:"exchangeCount"`
}

// summaryJSON is the wire shape of one Summary, used both for a single
// exchange (GET /exchanges/{id}) and for each entry of a list (GET
// /exchanges, and the "data:" line of GET /stream).
type summaryJSON struct {
	ID            uint64 `json:"id"`
	ConnID        uint64 `json:"connId"`
	ClientKey     string `json:"clientKey"`
	Started       string `json:"started"`
	Ended         string `json:"ended"`
	Mark          string `json:"mark"`
	Error         string `json:"error,omitempty"`
	HandlerRuns   int    `json:"handlerRuns"`
	Method        string `json:"method,omitempty"`
	Path          string `json:"path,omitempty"`
	Status        int    `json:"status,omitempty"`
	ReqTrueLen    int64  `json:"reqTrueLen"`
	RespTrueLen   int64  `json:"respTrueLen"`
	ReqStored     int64  `json:"reqStored"`
	RespStored    int64  `json:"respStored"`
	ReqTruncated  bool   `json:"reqTruncated"`
	RespTruncated bool   `json:"respTruncated"`
}

// statsJSON is the wire shape of Stats.
type statsJSON struct {
	DroppedQueueFull     uint64 `json:"droppedQueueFull"`
	DroppedWriteError    uint64 `json:"droppedWriteError"`
	Abandoned            uint64 `json:"abandoned"`
	Truncated            uint64 `json:"truncated"`
	EvictedSegments      uint64 `json:"evictedSegments"`
	IndexMemoryEvictions uint64 `json:"indexMemoryEvictions"`
	SlowSubscribers      uint64 `json:"slowSubscribers"`
	BytesOnDisk          int64  `json:"bytesOnDisk"`
	IndexEntries         int    `json:"indexEntries"`
	IndexApproxBytes     int64  `json:"indexApproxBytes"`
	DuplicateIndexIDs    uint64 `json:"duplicateIndexIds"`
}

const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func toClientJSON(c ClientSummary) clientJSON {
	return clientJSON{
		Key:           c.Key,
		FirstSeen:     c.FirstSeen.Format(rfc3339Nano),
		LastSeen:      c.LastSeen.Format(rfc3339Nano),
		ExchangeCount: c.ExchangeCount,
	}
}

func toSummaryJSON(sum Summary) summaryJSON {
	return summaryJSON{
		ID:            sum.ID,
		ConnID:        sum.ConnID,
		ClientKey:     sum.ClientKey,
		Started:       sum.Started.Format(rfc3339Nano),
		Ended:         sum.Ended.Format(rfc3339Nano),
		Mark:          sum.Mark.String(),
		Error:         sum.Error,
		HandlerRuns:   sum.HandlerRuns,
		Method:        sum.Method,
		Path:          sum.Path,
		Status:        sum.Status,
		ReqTrueLen:    sum.ReqTrueLen,
		RespTrueLen:   sum.RespTrueLen,
		ReqStored:     sum.ReqStored,
		RespStored:    sum.RespStored,
		ReqTruncated:  sum.ReqTruncated,
		RespTruncated: sum.RespTruncated,
	}
}

func toStatsJSON(st Stats) statsJSON {
	return statsJSON{
		DroppedQueueFull:     st.DroppedQueueFull,
		DroppedWriteError:    st.DroppedWriteError,
		Abandoned:            st.Abandoned,
		Truncated:            st.Truncated,
		EvictedSegments:      st.EvictedSegments,
		IndexMemoryEvictions: st.IndexMemoryEvictions,
		SlowSubscribers:      st.SlowSubscribers,
		BytesOnDisk:          st.BytesOnDisk,
		IndexEntries:         st.IndexEntries,
		IndexApproxBytes:     st.IndexApproxBytes,
		DuplicateIndexIDs:    st.DuplicateIndexIDs,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with code and a short plain message: never a stack
// trace, never an internal path (data-invariants: no sensitive detail
// crosses a trust boundary in an error message).
func writeError(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

func (s *Store) handleClients(w http.ResponseWriter, r *http.Request) {
	clients := s.Clients()
	out := make([]clientJSON, len(clients))
	for i, c := range clients {
		out[i] = toClientJSON(c)
	}
	writeJSON(w, map[string]any{"clients": out})
}

func (s *Store) handleExchangeList(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	if client == "" {
		writeError(w, http.StatusBadRequest, "client is required")
		return
	}

	after, err := parseAfter(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "after must be a non-negative integer")
		return
	}

	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit must be a positive integer")
		return
	}

	exchanges := s.Exchanges(client, after, limit)
	out := make([]summaryJSON, len(exchanges))
	for i, e := range exchanges {
		out[i] = toSummaryJSON(e)
	}
	writeJSON(w, map[string]any{"exchanges": out})
}

// parseAfter parses the after= query parameter: "" means 0 (no lower
// bound), anything else must be a valid uint64.
func parseAfter(v string) (uint64, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

// parseLimit parses the limit= query parameter against this route's own
// bound (defaultExchangesLimit, maxExchangesLimit): "" means the default,
// a non-positive value is an error rather than "unbounded", and anything
// above the max is silently clamped to it.
func parseLimit(v string) (int, error) {
	if v == "" {
		return defaultExchangesLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		if err == nil {
			err = errNonPositiveLimit
		}
		return 0, err
	}
	if n > maxExchangesLimit {
		n = maxExchangesLimit
	}
	return n, nil
}

var errNonPositiveLimit = &limitError{"limit must be positive"}

type limitError struct{ msg string }

func (e *limitError) Error() string { return e.msg }

// lookupExchangeID parses id from the request's {id} path value and
// reports whether the caller has already written a response (a 400 on a
// parse failure).
func lookupExchangeID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a non-negative integer")
		return 0, false
	}
	return id, true
}

// writeExchangeError maps the two Store.Exchange error cases to their
// wire status: ErrNotFound (never indexed) is 404, and ErrEvicted
// (indexed, since evicted, or lost the race with eviction) is 410. Any
// other error is a defect in this mapping, not a caller mistake, so it
// still answers with a short plain message rather than leaking detail.
func writeExchangeError(w http.ResponseWriter, err error) {
	switch err {
	case ErrNotFound:
		writeError(w, http.StatusNotFound, "exchange not found")
	case ErrEvicted:
		writeError(w, http.StatusGone, "exchange evicted")
	default:
		writeError(w, http.StatusInternalServerError, "exchange lookup failed")
	}
}

func (s *Store) handleExchangeSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := lookupExchangeID(w, r)
	if !ok {
		return
	}
	ex, err := s.Exchange(id)
	if err != nil {
		writeExchangeError(w, err)
		return
	}
	writeJSON(w, toSummaryJSON(summaryOf(ex)))
}

// summaryOf reduces a full Exchange (bytes included) to the Summary shape
// the JSON contract uses for both the list and single-exchange routes, so
// handleExchangeSummary never has to read Store's own index directly.
func summaryOf(ex Exchange) Summary {
	return Summary{
		ID:            ex.ID,
		ConnID:        ex.ConnID,
		ClientKey:     ex.ClientLFDI,
		Started:       ex.Started,
		Ended:         ex.Ended,
		Mark:          ex.Mark,
		Error:         ex.Error,
		HandlerRuns:   ex.HandlerRuns,
		ReqTrueLen:    ex.Request.TrueLen,
		RespTrueLen:   ex.Response.TrueLen,
		ReqStored:     int64(len(ex.Request.Bytes)),
		RespStored:    int64(len(ex.Response.Bytes)),
		ReqTruncated:  ex.Request.Truncated,
		RespTruncated: ex.Response.Truncated,
		Method:        firstLineMethod(ex.Request.Bytes),
		Path:          firstLinePath(ex.Request.Bytes),
		Status:        parseStatusLine(ex.Response.Bytes),
	}
}

func firstLineMethod(b []byte) string { m, _ := parseRequestLine(b); return m }
func firstLinePath(b []byte) string   { _, p := parseRequestLine(b); return p }

type exchangeDirection int

const (
	directionRequest exchangeDirection = iota
	directionResponse
)

// handleExchangeDirection returns the download handler for one direction:
// the exact bytes Store.Exchange returns for it, as
// application/octet-stream with Content-Disposition: attachment and
// X-Content-Type-Options: nosniff, so a browser never renders captured
// device traffic as HTML or executes it (data-invariants: certificate
// material is never in these bytes per the design's Q3, but arbitrary
// device payloads are, and nosniff plus attachment keeps them inert).
func (s *Store) handleExchangeDirection(dir exchangeDirection) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := lookupExchangeID(w, r)
		if !ok {
			return
		}
		ex, err := s.Exchange(id)
		if err != nil {
			writeExchangeError(w, err)
			return
		}
		var body []byte
		var name string
		if dir == directionRequest {
			body = ex.Request.Bytes
			name = "request"
		} else {
			body = ex.Response.Bytes
			name = "response"
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="exchange-`+strconv.FormatUint(id, 10)+"-"+name+`.bin"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
	}
}

func (s *Store) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, toStatsJSON(s.Stats()))
}
