package commitment

import "math/big"

// scaledTerm is one addend in a magnitude sum: |value| * factor * 10^multiplier.
// A bound is a scaledTerm too (factor 1, or 3600 for the watt-hour to
// watt-second conversion rule 7 needs), so every unit conversion goes
// through the same math/big scaling and never divides or overflows int64.
type scaledTerm struct {
	value      int64
	multiplier int8
	factor     int64
}

// magnitudeSumExceeds reports whether the exact sum of terms exceeds bound.
// Every term, bound included, is scaled to the lowest multiplier present by
// multiplying the others UP with an integer power of ten, so the
// comparison never divides and never rounds. This generalizes
// internal/flowreservation.magnitudeExceeds, which compares exactly two
// values the same way, to a sum of any number of them plus an arbitrary
// integer factor per term (design 5.3 rules 6 and 7 each sum several
// executions, scaled by Reach or by Reach*duration, against one bound).
func magnitudeSumExceeds(terms []scaledTerm, bound scaledTerm) bool {
	exp := bound.multiplier
	for _, t := range terms {
		if t.multiplier < exp {
			exp = t.multiplier
		}
	}

	sum := new(big.Int)
	for _, t := range terms {
		sum.Add(sum, scaleUp(t.value, t.multiplier, exp, t.factor))
	}
	return sum.Cmp(scaleUp(bound.value, bound.multiplier, exp, bound.factor)) > 0
}

// scaleUp returns |value| * factor * 10^(multiplier-exp). multiplier must be
// >= exp, so the only operation ever applied is a multiply.
func scaleUp(value int64, multiplier, exp int8, factor int64) *big.Int {
	v := new(big.Int).Abs(big.NewInt(value))
	if factor != 1 {
		v.Mul(v, big.NewInt(factor))
	}
	if multiplier > exp {
		v.Mul(v, pow10(int64(multiplier)-int64(exp)))
	}
	return v
}

func pow10(exp int64) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(exp), nil)
}

// ratScaled returns |value| * factor * 10^multiplier exactly, as a Rat so a
// negative multiplier loses nothing.
func ratScaled(value int64, multiplier int8, factor int64) *big.Rat {
	n := new(big.Int).Abs(big.NewInt(value))
	n.Mul(n, big.NewInt(factor))
	if multiplier >= 0 {
		return new(big.Rat).SetInt(n.Mul(n, pow10(int64(multiplier))))
	}
	return new(big.Rat).SetFrac(n, pow10(-int64(multiplier)))
}
