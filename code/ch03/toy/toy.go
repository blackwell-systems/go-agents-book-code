// Package toy is a language model small enough to read: it predicts the
// next word from the two before it, using counts taken from a text.
package toy

import (
	"cmp"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
)

// tag::model[]

// A Model counts, for each pair of words in its training text, which
// words came next and how often.
type Model struct {
	next map[[2]string]map[string]int
}

// Train builds a Model from text, split into words at spaces and line
// breaks.
func Train(text string) *Model {
	m := &Model{next: map[[2]string]map[string]int{}}
	words := strings.Fields(text)
	for i := 2; i < len(words); i++ {
		pair := [2]string{words[i-2], words[i-1]}
		if m.next[pair] == nil {
			m.next[pair] = map[string]int{}
		}
		m.next[pair][words[i]]++ // <1>
	}
	return m
}

// end::model[]

// tag::next[]

// A Choice is a possible next word and its probability.
type Choice struct {
	Word string
	P    float64
}

// Next returns the words that can follow a and b, most likely first,
// with their probabilities at the given temperature, which must be
// above 0. It returns nil for a pair the text never had.
func (m *Model) Next(a, b string, temperature float64) []Choice {
	counts := m.next[[2]string{a, b}]
	if len(counts) == 0 {
		return nil
	}
	top := float64(slices.Max(slices.Collect(maps.Values(counts))))
	var choices []Choice
	var total float64
	for word, n := range counts {
		w := math.Pow(float64(n)/top, 1/temperature) // <1>
		choices = append(choices, Choice{word, w})
		total += w
	}
	// end::next[]
	// tag::next-normalize[]
	for i := range choices {
		choices[i].P /= total // <1>
	}
	slices.SortFunc(choices, func(x, y Choice) int {
		return cmp.Or(cmp.Compare(y.P, x.P),
			strings.Compare(x.Word, y.Word)) // ties in a fixed order
	})
	return choices
}

// end::next-normalize[]

// tag::generate[]

// Generate continues from the words a and b for up to n more words.
// At temperature 0 it always takes the most likely word; above 0 it
// samples from the probabilities, using rng.
func (m *Model) Generate(a, b string, n int, temperature float64,
	rng *rand.Rand) []string {
	out := []string{a, b}
	for range n {
		choices := m.Next(a, b, cmp.Or(temperature, 1))
		if len(choices) == 0 {
			break // a pair the text never had
		}
		word := choices[0].Word // <1>
		if temperature > 0 {
			word = sample(choices, rng.Float64()) // <2>
		}
		out = append(out, word)
		a, b = b, word
	}
	return out
}

// end::generate[]

// tag::sample[]

// sample picks a choice at random in proportion to its probability:
// r, between 0 and 1, lands in one choice's share of the line.
func sample(choices []Choice, r float64) string {
	for _, c := range choices {
		if r < c.P {
			return c.Word
		}
		r -= c.P
	}
	return choices[len(choices)-1].Word // r was 1 less rounding error
}

// end::sample[]
