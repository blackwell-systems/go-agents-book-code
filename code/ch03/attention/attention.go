// Package attention holds the arithmetic inside a transformer, on
// vectors small enough to check by hand: similarity, softmax, and one
// attention step.
package attention

import (
	"math"
	"slices"
)

// tag::cosine[]

// Dot returns the dot product of two vectors of the same length:
// multiply the numbers in matching positions and add up the results.
func Dot(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// Cosine returns how closely two vectors point the same way: 1 for the
// same direction, 0 for unrelated, -1 for opposite. Dividing by the
// vectors' sizes leaves only their direction. Neither may be all zeros.
func Cosine(a, b []float64) float64 {
	return Dot(a, b) / math.Sqrt(Dot(a, a)*Dot(b, b))
}

// end::cosine[]

// tag::softmax[]

// Softmax turns scores into probabilities: each becomes e to the power
// of the score, divided by the total, so they sum to 1 and a higher
// score always gets a higher probability. At least one score must be
// finite; a score of -Inf gets probability 0, which is how attention
// hides positions it may not look at.
func Softmax(scores []float64) []float64 {
	top := slices.Max(scores)
	probs := make([]float64, len(scores))
	var total float64
	for i, s := range scores {
		probs[i] = math.Exp(s - top) // <1>
		total += probs[i]
	}
	for i := range probs {
		probs[i] /= total
	}
	return probs
}

// WithTemperature divides scores by the temperature, the step a model
// takes before softmax when it samples.
func WithTemperature(scores []float64, t float64) []float64 {
	out := make([]float64, len(scores))
	for i, s := range scores {
		out[i] = s / t
	}
	return out
}

// end::softmax[]

// tag::attend[]

// Attend runs one attention step for one position. It scores each
// earlier position's key against the query, turns the scores into
// weights with softmax, and returns the weights and the values blended
// by those weights. There must be one value for each key, and at least
// one of each.
func Attend(query []float64, keys, values [][]float64) (
	weights, blend []float64) {
	scale := math.Sqrt(float64(len(query)))
	scores := make([]float64, len(keys))
	for i, k := range keys {
		scores[i] = Dot(query, k) / scale // <1>
	}
	weights = Softmax(scores)
	blend = make([]float64, len(values[0]))
	for i, v := range values {
		for j := range v {
			blend[j] += weights[i] * v[j] // <2>
		}
	}
	return weights, blend
}

// end::attend[]
