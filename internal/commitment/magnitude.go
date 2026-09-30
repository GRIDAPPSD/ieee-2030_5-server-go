package commitment

import "math/big"

// scaledTerm is one addend in a magnitude sum: |value| * factor * 10^multiplier.
type scaledTerm struct {
	value      int64
	multiplier int8
	factor     int64
}

// magnitudeSumExceeds reports whether the exact sum of terms exceeds
// |boundValue| * 10^boundMultiplier. Every term is scaled to the lowest
// multiplier present (across the terms and the bound) by multiplying the
// others UP with an integer power of ten, so the comparison never divides
// and never rounds. This generalizes
// internal/flowreservation.magnitudeExceeds, which compares exactly two
// values the same way, to a sum of any number of them (design 5.3 rules 6
// and 7 each sum several executions against one bound).
func magnitudeSumExceeds(terms []scaledTerm, boundValue int64, boundMultiplier int8) bool {
	exp := boundMultiplier
	for _, t := range terms {
		if t.multiplier < exp {
			exp = t.multiplier
		}
	}

	sum := new(big.Int)
	for _, t := range terms {
		sum.Add(sum, scaleUp(t.value, t.multiplier, exp, t.factor))
	}
	bound := scaleUp(boundValue, boundMultiplier, exp, 1)
	return sum.Cmp(bound) > 0
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
