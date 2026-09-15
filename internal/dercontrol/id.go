package dercontrol

import "fmt"

// idBase bounds the decimal width used to encode a Unix timestamp and its
// complement in the sortable id below. 10^11 comfortably outlives any
// timestamp this server will see (it wraps in the year 5138).
const idBase = 100_000_000_000

// sortableID builds a store id whose ascending byte order equals IEEE
// 2030.5-2018 Table 50's DERControl order: interval.start ascending, then
// creationTime descending, then mRID descending (acceptance criterion 6).
//
// The scheme: an 11-digit zero-padded start, then an 11-digit zero-padded
// complement of creationTime (so ascending order of the complement is
// descending order of creationTime), then mRID with every hex digit
// complemented against 15 (so ascending order of the complement is
// descending order of the mRID).
func sortableID(start, creationTime int64, mrid string) string {
	return fmt.Sprintf("%011d%011d%s", start, idBase-1-creationTime, invertHex(mrid))
}

// invertHex maps each hex digit d to 15-d, preserving case as uppercase.
// mrid is always the 32-uppercase-hex-digit output of newMRID.
func invertHex(mrid string) string {
	out := make([]byte, len(mrid))
	for i := 0; i < len(mrid); i++ {
		out[i] = hexDigit(15 - hexValue(mrid[i]))
	}
	return string(out)
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}

func hexDigit(v byte) byte {
	if v < 10 {
		return '0' + v
	}
	return 'A' + (v - 10)
}
