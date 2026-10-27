package dercontrol

import (
	"errors"
	"math/big"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// #1156: one grid power request split across aggregators by weight.

// SplitShare is one weight's part of a split request.
type SplitShare struct {
	// Units is the share in units of the request's 10^y W, before encoding.
	// The Units of one split sum to the request's x.
	Units int64
	// TargetW is the share as opModTargetW carries it, signed by the
	// request's direction, or nil when Units is zero.
	TargetW *sep2.ActivePower
}

// DefaultMaxNameplateW is the default ceiling on one device's rtgMaxW as a
// split weight (#1156): a device reporting more is weighted as one with no
// nameplate, so no device can steer a split by overstating its rating.
const DefaultMaxNameplateW = 1_000_000

// Errors SplitTarget refuses a request with.
var (
	ErrSplitRequest     = errors.New("dercontrol: split needs x of at least 1, an allowed multiplier y, a sign of 1 or -1 and positive weights")
	ErrShareUnencodable = errors.New("dercontrol: a share does not fit an int16 value at any allowed multiplier")
)

// SplitTarget splits x * 10^y W over weights in proportion. Shares are
// apportioned in whole units of 10^y W by largest remainder, ties to the
// lower index, so they sum to x exactly. Each is then encoded at the
// smallest multiplier m >= y whose value, rounded half away from zero, fits
// int16, so no share is finer than the request. sign is 1 for discharge
// (positive on the wire) and -1 for charge.
func SplitTarget(x int64, y int8, sign int, weights []*big.Rat) ([]SplitShare, error) {
	if x < 1 || !ValidMultiplier(int64(y)) || (sign != 1 && sign != -1) || len(weights) == 0 {
		return nil, ErrSplitRequest
	}
	total := new(big.Rat)
	for _, w := range weights {
		if w == nil || w.Sign() <= 0 {
			return nil, ErrSplitRequest
		}
		total.Add(total, w)
	}

	units := make([]int64, len(weights))
	remainders := make([]*big.Rat, len(weights))
	left := x
	for n, w := range weights {
		q := new(big.Rat).Mul(big.NewRat(x, 1), w)
		q.Quo(q, total)
		whole := new(big.Int).Quo(q.Num(), q.Denom())
		units[n] = whole.Int64()
		remainders[n] = q.Sub(q, new(big.Rat).SetInt(whole))
		left -= units[n]
	}
	order := make([]int, len(weights))
	for n := range order {
		order[n] = n
	}
	slices.SortStableFunc(order, func(a, b int) int { return remainders[b].Cmp(remainders[a]) })
	for _, n := range order[:left] {
		units[n]++
	}

	out := make([]SplitShare, len(weights))
	for n, u := range units {
		out[n].Units = u
		if u == 0 {
			continue
		}
		p, err := encodeUnits(u, y, sign)
		if err != nil {
			return nil, err
		}
		out[n].TargetW = &p
	}
	return out, nil
}

// encodeUnits is u * 10^y W at the smallest multiplier m >= y that fits.
func encodeUnits(u int64, y int8, sign int) (sep2.ActivePower, error) {
	div := int64(1)
	for m := int64(y); m <= powerOfTen.max; m++ {
		if v := (u + div/2) / div; v <= int16Range.max {
			return sep2.ActivePower{Value: int16(int64(sign) * v), Multiplier: int8(m)}, nil
		}
		div *= 10
	}
	return sep2.ActivePower{}, ErrShareUnencodable
}
