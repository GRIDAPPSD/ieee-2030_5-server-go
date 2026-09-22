package sep2capture

import (
	"bytes"
	"testing"
	"time"
)

// TestRecordHeaderRoundTrips proves the codec itself: encode then decode
// returns every field encodeRecordHeader wrote, byte for byte where it
// matters (the id, conn id, and both lengths that Exchange relies on to
// locate a record's payload).
func TestRecordHeaderRoundTrips(t *testing.T) {
	started := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	ended := started.Add(3 * time.Second)
	ex := Exchange{
		ID:       42,
		ConnID:   7,
		Started:  started,
		Ended:    ended,
		Request:  Direction{Bytes: []byte("GET / HTTP/1.1\r\n\r\n")},
		Response: Direction{Bytes: []byte("HTTP/1.1 200 OK\r\n\r\n")},
	}

	h := encodeRecordHeader(ex)
	if len(h) != recordHeaderLen {
		t.Fatalf("encodeRecordHeader: got %d bytes, want %d", len(h), recordHeaderLen)
	}
	if !bytes.Equal(h[0:8], []byte(recordMagic)) {
		t.Fatalf("magic: got %q, want %q", h[0:8], recordMagic)
	}

	d, err := decodeRecordHeader(h)
	if err != nil {
		t.Fatalf("decodeRecordHeader: %v", err)
	}
	if d.ExchangeID != ex.ID {
		t.Errorf("ExchangeID: got %d, want %d", d.ExchangeID, ex.ID)
	}
	if d.ConnID != ex.ConnID {
		t.Errorf("ConnID: got %d, want %d", d.ConnID, ex.ConnID)
	}
	if d.StartedNS != started.UnixNano() {
		t.Errorf("StartedNS: got %d, want %d", d.StartedNS, started.UnixNano())
	}
	if d.EndedNS != ended.UnixNano() {
		t.Errorf("EndedNS: got %d, want %d", d.EndedNS, ended.UnixNano())
	}
	if int(d.ReqLen) != len(ex.Request.Bytes) {
		t.Errorf("ReqLen: got %d, want %d", d.ReqLen, len(ex.Request.Bytes))
	}
	if int(d.RespLen) != len(ex.Response.Bytes) {
		t.Errorf("RespLen: got %d, want %d", d.RespLen, len(ex.Response.Bytes))
	}
}

// TestDecodeRecordHeaderRejectsBadMagic is the codec's own boundary check:
// an offline reader (or a corrupted segment) must not be parsed as if it
// were a real record.
func TestDecodeRecordHeaderRejectsBadMagic(t *testing.T) {
	h := encodeRecordHeader(Exchange{})
	h[0] = 'X'
	if _, err := decodeRecordHeader(h); err != errBadRecordHeader {
		t.Fatalf("decodeRecordHeader with bad magic: got %v, want errBadRecordHeader", err)
	}
	if _, err := decodeRecordHeader(h[:recordHeaderLen-1]); err != errBadRecordHeader {
		t.Fatalf("decodeRecordHeader with short header: got %v, want errBadRecordHeader", err)
	}
}
