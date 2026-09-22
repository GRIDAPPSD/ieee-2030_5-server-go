package sep2capture

import (
	"encoding/binary"
	"errors"
)

// recordHeaderLen is Q4's fixed on-disk header: magic(8) version(1)
// exchange id(8) conn id(8) started(8) ended(8) req len(4) resp len(4).
const (
	recordMagic     = "S2CAPREC"
	recordVersion   = 1
	recordHeaderLen = 8 + 1 + 8 + 8 + 8 + 8 + 4 + 4
)

// errBadRecordHeader is returned by decodeRecordHeader for anything that
// is not a well-formed header: too short, or missing the magic.
var errBadRecordHeader = errors.New("sep2capture: bad record header")

// encodeRecordHeader builds the header that makes a segment file
// self-describing offline, before the request and response bytes that
// follow it exactly as captured. Behavior, identity, and truncation are
// not in this header: they live only in the in-memory index, the source
// of truth while the process runs (Q4). This header is for a human reading
// a segment file after a crash, not for anything this package reads back
// at runtime (Exchange reads by the index's stored offset and length).
func encodeRecordHeader(ex Exchange) []byte {
	h := make([]byte, recordHeaderLen)
	copy(h[0:8], recordMagic)
	h[8] = recordVersion
	binary.BigEndian.PutUint64(h[9:17], ex.ID)
	binary.BigEndian.PutUint64(h[17:25], ex.ConnID)
	binary.BigEndian.PutUint64(h[25:33], uint64(ex.Started.UnixNano()))
	binary.BigEndian.PutUint64(h[33:41], uint64(ex.Ended.UnixNano()))
	binary.BigEndian.PutUint32(h[41:45], uint32(len(ex.Request.Bytes)))
	binary.BigEndian.PutUint32(h[45:49], uint32(len(ex.Response.Bytes)))
	return h
}

// decodedRecordHeader is decodeRecordHeader's result: an offline reader's
// or this package's own codec test's view of one record's header.
type decodedRecordHeader struct {
	Version    uint8
	ExchangeID uint64
	ConnID     uint64
	StartedNS  int64
	EndedNS    int64
	ReqLen     uint32
	RespLen    uint32
}

func decodeRecordHeader(b []byte) (decodedRecordHeader, error) {
	if len(b) < recordHeaderLen || string(b[0:8]) != recordMagic {
		return decodedRecordHeader{}, errBadRecordHeader
	}
	return decodedRecordHeader{
		Version:    b[8],
		ExchangeID: binary.BigEndian.Uint64(b[9:17]),
		ConnID:     binary.BigEndian.Uint64(b[17:25]),
		StartedNS:  int64(binary.BigEndian.Uint64(b[25:33])),
		EndedNS:    int64(binary.BigEndian.Uint64(b[33:41])),
		ReqLen:     binary.BigEndian.Uint32(b[41:45]),
		RespLen:    binary.BigEndian.Uint32(b[45:49]),
	}, nil
}
