package adminplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// streamFake is a stream panel whose Open records each request and hands
// its send func to the test.
type streamFake struct {
	opens   atomic.Int32
	mu      sync.Mutex
	reqs    []sep2admin.StreamRequest
	sends   chan sep2admin.StreamSendFunc
	onOpen  func(ctx context.Context, req sep2admin.StreamRequest, send sep2admin.StreamSendFunc) error
	openErr error
}

func newStreamFake() *streamFake { return &streamFake{sends: make(chan sep2admin.StreamSendFunc, 16)} }

func (f *streamFake) panel(id string) sep2admin.Panel {
	p := testPanel(id, 1, okView)
	p.Stream = &sep2admin.Stream{
		Param: sep2admin.StreamParam{MaxLen: 16, Charset: "abcdefghijklmnopqrstuvwxyz/._-"},
		Open: func(ctx context.Context, req sep2admin.StreamRequest, send sep2admin.StreamSendFunc) error {
			f.opens.Add(1)
			f.mu.Lock()
			f.reqs = append(f.reqs, req)
			f.mu.Unlock()
			if f.onOpen != nil {
				if err := f.onOpen(ctx, req, send); err != nil {
					return err
				}
			}
			f.sends <- send
			return f.openErr
		},
	}
	return p
}

func (f *streamFake) lastReq() sep2admin.StreamRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

// streamPlane serves panels on a real listener so streaming, deadlines and
// the credential chain all run as in production. Loopback bypass is off,
// so every request needs a real credential.
type streamPlane struct {
	srv      *httptest.Server
	tickets  *auth.TicketStore
	sessions *auth.SessionStore
	cookie   string
}

func newStreamPlane(t *testing.T, writeTimeout time.Duration, panels ...sep2admin.Panel) *streamPlane {
	t.Helper()
	sp := &streamPlane{tickets: auth.NewTicketStore(30 * time.Second), sessions: auth.NewSessionStore(time.Minute, time.Hour)}
	h, _, err := Build(Config{AdminKey: panelTestKey, Panels: panels, Tickets: sp.tickets, Sessions: sp.sessions})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if sp.cookie, err = sp.sessions.Issue(); err != nil {
		t.Fatal(err)
	}
	sp.srv = httptest.NewUnstartedServer(h)
	sp.srv.Config.WriteTimeout = writeTimeout
	sp.srv.Start()
	t.Cleanup(sp.srv.Close)
	return sp
}

// open requests a stream with the session cookie, as EventSource does.
func (sp *streamPlane) open(t *testing.T, ctx context.Context, path string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sp.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: sp.cookie})
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := sp.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

type sseFrame struct {
	id    string
	hasID bool
	data  streamEventJSON
}

// readFrames reads n data frames, skipping heartbeat comments, and reports
// how many heartbeats it passed.
func readFrames(t *testing.T, r *bufio.Reader, n int) ([]sseFrame, int) {
	t.Helper()
	var frames []sseFrame
	heartbeats := 0
	var cur sseFrame
	var hasData bool
	for len(frames) < n {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read after %d frames: %v", len(frames), err)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if hasData {
				frames = append(frames, cur)
			}
			cur, hasData = sseFrame{}, false
		case line == ": keep-alive":
			heartbeats++
		case strings.HasPrefix(line, "id: "):
			cur.id, cur.hasID = strings.TrimPrefix(line, "id: "), true
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.data); err != nil {
				t.Fatalf("data line %q: %v", line, err)
			}
			hasData = true
		default:
			t.Fatalf("unexpected SSE line %q", line)
		}
	}
	return frames, heartbeats
}

// refusal reads a response expected to be a refusal, bounded so a stream
// opened by mistake fails the test instead of hanging it.
func refusal(t *testing.T, sp *streamPlane, path string, header map[string]string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, path, header)
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") == "text/event-stream" {
		return "(a stream opened)", resp.StatusCode
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading refusal body: %v", err)
	}
	return string(body), resp.StatusCode
}

func expectEOF(t *testing.T, r *bufio.Reader) {
	t.Helper()
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading to the end: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("bytes after the final event: %q", rest)
	}
}

func recvSend(t *testing.T, f *streamFake) sep2admin.StreamSendFunc {
	t.Helper()
	select {
	case s := <-f.sends:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("Open was not called")
		return nil
	}
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 123000000, time.FixedZone("x", 3600))

func msg(id uint64, text string) sep2admin.StreamEvent {
	return sep2admin.StreamEvent{ID: id, Time: t0, Kind: sep2admin.StreamMessage, Text: text}
}

func TestPanelStreamWithSessionCookie(t *testing.T) {
	f := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param="+url.QueryEscape("/topic/a.b"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, Content-Type %q; want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	send := recvSend(t, f)
	if got := f.lastReq(); got != (sep2admin.StreamRequest{Param: "/topic/a.b", After: 0}) {
		t.Fatalf("Open got %+v, want param /topic/a.b after 0", got)
	}
	if !send(msg(1, "hello")) || !send(sep2admin.StreamEvent{ID: 2, Time: t0, Kind: sep2admin.StreamStatus, Text: "reconnecting"}) {
		t.Fatal("send refused on an open stream")
	}
	frames, _ := readFrames(t, bufio.NewReader(resp.Body), 2)
	want := []sseFrame{
		{id: "1", hasID: true, data: streamEventJSON{ID: "1", Time: "2026-10-05T11:00:00.123Z", Kind: "message", Text: "hello"}},
		{id: "2", hasID: true, data: streamEventJSON{ID: "2", Time: "2026-10-05T11:00:00.123Z", Kind: "status", Text: "reconnecting"}},
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Errorf("frame %d = %+v, want %+v", i, frames[i], want[i])
		}
	}
}

func TestPanelStreamRefusesATicket(t *testing.T) {
	f := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"))
	ticket, err := sp.tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sp.srv.Client().Get(sp.srv.URL + "/api/ui/panels/bus/stream?param=a&ticket=" + ticket)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || string(body) != `{"error":"panel streams do not accept a ticket"}` {
		t.Fatalf("ticket stream = %d %s, want 401 with the ticket refusal", resp.StatusCode, body)
	}
	if n := f.opens.Load(); n != 0 {
		t.Fatalf("Open ran %d times for a ticket-admitted request", n)
	}
	// Control: the same request with a Bearer key and no ticket opens.
	req, _ := http.NewRequest(http.MethodGet, sp.srv.URL+"/api/ui/panels/bus/stream?param=a", nil)
	req.Header.Set("Authorization", "Bearer "+panelTestKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err = sp.srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bearer stream = %d, want 200", resp.StatusCode)
	}
}

func TestPanelStreamRefusesWithNoCredential(t *testing.T) {
	f := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"))
	resp, err := sp.srv.Client().Get(sp.srv.URL + "/api/ui/panels/bus/stream?param=a")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || f.opens.Load() != 0 {
		t.Fatalf("no credential = %d with %d opens, want 401 and none", resp.StatusCode, f.opens.Load())
	}
}

func TestPanelStreamRefusesAParameterBeforeOpen(t *testing.T) {
	f := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"), testPanel("plain", 2, okView))
	cases := map[string]string{
		"missing":          "/api/ui/panels/bus/stream",
		"empty":            "/api/ui/panels/bus/stream?param=",
		"over MaxLen":      "/api/ui/panels/bus/stream?param=" + strings.Repeat("a", 17),
		"outside charset":  "/api/ui/panels/bus/stream?param=" + url.QueryEscape("/topic/*"),
		"control byte":     "/api/ui/panels/bus/stream?param=a%0A",
		"repeated":         "/api/ui/panels/bus/stream?param=a&param=b",
		"extra key":        "/api/ui/panels/bus/stream?param=a&x=1",
		"bad escape":       "/api/ui/panels/bus/stream?param=%zz",
		"query over bytes": "/api/ui/panels/bus/stream?param=a&" + strings.Repeat("x", maxStreamQueryBytes),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			body, code := refusal(t, sp, path, nil)
			if code != http.StatusBadRequest || body != `{"error":"invalid stream parameter"}` {
				t.Fatalf("= %d %s, want 400 invalid stream parameter", code, body)
			}
		})
	}
	if n := f.opens.Load(); n != 0 {
		t.Fatalf("Open ran %d times for refused parameters", n)
	}
	for _, path := range []string{"/api/ui/panels/plain/stream?param=a", "/api/ui/panels/nope/stream?param=a"} {
		resp := sp.open(t, context.Background(), path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
	// Control: a value at MaxLen, every byte from the charset, opens.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=/topic/abc.d_e-f", nil)
	defer resp.Body.Close()
	recvSend(t, f)
	if resp.StatusCode != http.StatusOK || f.lastReq().Param != "/topic/abc.d_e-f" {
		t.Fatalf("valid parameter = %d, Open got %q", resp.StatusCode, f.lastReq().Param)
	}
}

func TestPanelStreamCapIsEightPerPlane(t *testing.T) {
	f := newStreamFake()
	g := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"), g.panel("other"))
	var cancels []context.CancelFunc
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()
	for i := range maxOpenPanelStreams {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		id := "bus"
		if i%2 == 1 {
			id = "other"
		}
		resp := sp.open(t, ctx, "/api/ui/panels/"+id+"/stream?param=a", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d = %d, want 200", i+1, resp.StatusCode)
		}
	}
	if body, code := refusal(t, sp, "/api/ui/panels/bus/stream?param=a", nil); code != http.StatusServiceUnavailable || body != `{"error":"too many open panel streams"}` {
		t.Fatalf("ninth stream = %d %s, want 503", code, body)
	}
	if n := f.opens.Load() + g.opens.Load(); n != maxOpenPanelStreams {
		t.Fatalf("Open ran %d times, want %d: the ninth must be refused before Open", n, maxOpenPanelStreams)
	}
	// Closing one frees its slot once its handler returns.
	cancels[0]()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never freed: last status %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPanelStreamOverflowClosesWithAFinalStatus(t *testing.T) {
	f := newStreamFake()
	var mu sync.Mutex
	var results []bool
	f.onOpen = func(_ context.Context, _ sep2admin.StreamRequest, send sep2admin.StreamSendFunc) error {
		// Nothing is written until Open returns, so this fills the queue
		// exactly as a reader that stopped reading would.
		mu.Lock()
		defer mu.Unlock()
		for i := 1; i <= maxStreamQueueEvents+2; i++ {
			results = append(results, send(msg(uint64(i), "x")))
		}
		return nil
	}
	sp := newStreamPlane(t, 0, f.panel("bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", nil)
	defer resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(results) != maxStreamQueueEvents+2 {
		t.Fatalf("%d sends recorded", len(results))
	}
	for i, ok := range results {
		if want := i < maxStreamQueueEvents; ok != want {
			t.Fatalf("send %d = %v, want %v", i+1, ok, want)
		}
	}
	r := bufio.NewReader(resp.Body)
	frames, _ := readFrames(t, r, 1)
	got := frames[0]
	if got.hasID || got.data.ID != "" || got.data.Kind != "status" || got.data.Text != "stream closed: reader too slow" {
		t.Fatalf("frame = %+v, want only the id-less slow-reader status", got)
	}
	expectEOF(t, r)
}

// TestPanelStreamSlowReaderOverRealTCP stops reading until the source's
// send fails, then reads everything: the events written before the
// overflow arrive in order with no gap, then the final status, then EOF.
func TestPanelStreamSlowReaderOverRealTCP(t *testing.T) {
	f := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", nil)
	defer resp.Body.Close()
	send := recvSend(t, f)
	text := strings.Repeat("y", 60<<10)
	sent := 0
	for id := uint64(1); ; id++ {
		if !send(msg(id, text)) {
			break
		}
		sent++
		if sent > 2000 {
			t.Fatal("send never failed with nobody reading")
		}
	}
	if send(msg(uint64(sent+5), "late")) {
		t.Fatal("send after the overflow was accepted")
	}
	r := bufio.NewReader(resp.Body)
	var last sseFrame
	n := 0
	for {
		frames, _ := readFrames(t, r, 1)
		last = frames[0]
		if last.data.Kind == "status" {
			break
		}
		n++
		if last.id != strconv.Itoa(n) || last.data.Text != text {
			t.Fatalf("event %d has id %q, want %d with the full text", n, last.id, n)
		}
	}
	if last.hasID || last.data.Text != "stream closed: reader too slow" {
		t.Fatalf("final frame = %+v, want the slow-reader status", last)
	}
	if n == 0 || n >= sent {
		t.Fatalf("%d events delivered of %d accepted; want some, and the overflow dropped the rest", n, sent)
	}
	expectEOF(t, r)
}

func TestPanelStreamResumesFromLastEventID(t *testing.T) {
	f := newStreamFake()
	f.onOpen = func(_ context.Context, req sep2admin.StreamRequest, send sep2admin.StreamSendFunc) error {
		// A replay that overlaps what the client already saw.
		for id := req.After - 1; id <= req.After+2; id++ {
			if !send(msg(id, "e"+strconv.FormatUint(id, 10))) {
				return errors.New("send refused")
			}
		}
		return nil
	}
	sp := newStreamPlane(t, 0, f.panel("bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", map[string]string{"Last-Event-ID": "41"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || f.lastReq().After != 41 {
		t.Fatalf("status %d, Open After %d; want 200 and 41", resp.StatusCode, f.lastReq().After)
	}
	frames, _ := readFrames(t, bufio.NewReader(resp.Body), 2)
	if frames[0].id != "42" || frames[0].data.Text != "e42" || frames[1].id != "43" || frames[1].data.Text != "e43" {
		t.Fatalf("frames = %+v, want 42 then 43, nothing at or below 41", frames)
	}

	body, code := refusal(t, sp, "/api/ui/panels/bus/stream?param=a", map[string]string{"Last-Event-ID": "1-2"})
	if code != http.StatusBadRequest || f.opens.Load() != 1 {
		t.Fatalf("malformed Last-Event-ID = %d %s with %d opens, want 400 before Open", code, body, f.opens.Load())
	}
}

// TestPanelStreamOutlivesTheWriteTimeout holds a stream open well past the
// listener's WriteTimeout: heartbeats extend the deadline, so an event sent
// after it still arrives.
func TestPanelStreamOutlivesTheWriteTimeout(t *testing.T) {
	savedDeadline, savedBeat := panelStreamWriteDeadline, panelStreamHeartbeatInterval
	panelStreamWriteDeadline, panelStreamHeartbeatInterval = 300*time.Millisecond, 50*time.Millisecond
	defer func() { panelStreamWriteDeadline, panelStreamHeartbeatInterval = savedDeadline, savedBeat }()

	f := newStreamFake()
	sp := newStreamPlane(t, 200*time.Millisecond, f.panel("bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", nil)
	defer resp.Body.Close()
	send := recvSend(t, f)
	sent := make(chan bool, 1)
	go func() {
		time.Sleep(time.Second)
		sent <- send(msg(7, "late"))
	}()
	frames, beats := readFrames(t, bufio.NewReader(resp.Body), 1)
	if !<-sent {
		t.Fatal("stream ended before the late event")
	}
	if frames[0].id != "7" || frames[0].data.Text != "late" {
		t.Fatalf("frame = %+v, want the late event", frames[0])
	}
	if beats < 5 {
		t.Fatalf("%d heartbeats in a second at a 50ms interval, want at least 5", beats)
	}
}

func TestPanelStreamRefusesAnInvalidEvent(t *testing.T) {
	cases := map[string]sep2admin.StreamEvent{
		"over 64 KiB":   msg(1, strings.Repeat("z", sep2admin.MaxStreamEventBytes)),
		"id 0":          msg(0, "x"),
		"zero time":     {ID: 1, Kind: sep2admin.StreamMessage, Text: "x"},
		"unknown kind":  {ID: 1, Time: t0, Kind: "alert", Text: "x"},
		"invalid UTF-8": msg(1, "a\xff"),
	}
	for name, ev := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStreamFake()
			sp := newStreamPlane(t, 0, f.panel("bus"))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", nil)
			defer resp.Body.Close()
			send := recvSend(t, f)
			if send(ev) {
				t.Fatal("invalid event accepted")
			}
			r := bufio.NewReader(resp.Body)
			frames, _ := readFrames(t, r, 1)
			if frames[0].hasID || frames[0].data.Text != "stream closed: panel sent an invalid event" {
				t.Fatalf("frame = %+v, want the invalid-event status", frames[0])
			}
			expectEOF(t, r)
		})
	}
	// Control: the largest event that fits is delivered whole.
	f := newStreamFake()
	sp := newStreamPlane(t, 0, f.panel("bus"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := sp.open(t, ctx, "/api/ui/panels/bus/stream?param=a", nil)
	defer resp.Body.Close()
	send := recvSend(t, f)
	overhead := len(`{"id":"1","time":"2026-10-05T11:00:00.123Z","kind":"message","text":""}`)
	fit := strings.Repeat("z", sep2admin.MaxStreamEventBytes-overhead)
	if !send(msg(1, fit)) {
		t.Fatal("event of exactly 64 KiB refused")
	}
	frames, _ := readFrames(t, bufio.NewReader(resp.Body), 1)
	if frames[0].data.Text != fit {
		t.Fatalf("delivered text of %d bytes, want %d", len(frames[0].data.Text), len(fit))
	}
}

func TestPanelStreamOpenFailureAnswersWithoutItsText(t *testing.T) {
	f := newStreamFake()
	f.openErr = errors.New("broker password hunter2")
	sp := newStreamPlane(t, 0, f.panel("bus"))
	body, code := refusal(t, sp, "/api/ui/panels/bus/stream?param=a", nil)
	if code != http.StatusInternalServerError || body != `{"error":"panel failed"}` {
		t.Fatalf("= %d %s, want 500 panel failed with no error text", code, body)
	}
	if send := recvSend(t, f); send(msg(1, "x")) {
		t.Fatal("send accepted after the stream was refused")
	}
}

func TestPanelListCarriesStreamBounds(t *testing.T) {
	f := newStreamFake()
	rec := get(buildWithPanels(t, f.panel("bus"), testPanel("plain", 2, okView)), "/api/ui/panels", true)
	want := `[{"id":"bus","label":"Label bus","stream":{"maxLen":16,"charset":"abcdefghijklmnopqrstuvwxyz/._-"}},{"id":"plain","label":"Label plain"}]`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("list = %d %s, want %s", rec.Code, rec.Body, want)
	}
}
