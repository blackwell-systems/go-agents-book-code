package bpe

import (
	"slices"
	"strings"
	"testing"
)

func TestTrain(t *testing.T) {
	// "low" appears 5 times and "lower" 2 times, so the first merges
	// build " l", " lo", and " low".
	text := strings.Repeat(" low", 5) + strings.Repeat(" lower", 2)
	tok := Train(text, 3)
	want := []pair{{" ", "l"}, {" l", "o"}, {" lo", "w"}}
	if !slices.Equal(tok.merges, want) {
		t.Errorf("merges = %q, want %q", tok.merges, want)
	}
	if got := tok.Encode(" lower"); !slices.Equal(got, []string{" low", "e", "r"}) {
		t.Errorf("Encode(lower) = %q", got)
	}
	if got := tok.Vocab(); got != 259 {
		t.Errorf("Vocab = %d, want 259", got)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	text := "func Parse(s string) (time.Duration, error) {\n\treturn 0, nil\n}\n" +
		"Größe: ¿qué tal?"
	tok := Train(text, 50)
	if got := strings.Join(tok.Encode(text), ""); got != text {
		t.Errorf("tokens don't join back into the text:\n%q", got)
	}
}

func TestTrainIsRepeatable(t *testing.T) {
	text := " the then there these other thee th the"
	a, b := Train(text, 20), Train(text, 20)
	if !slices.Equal(a.merges, b.merges) {
		t.Errorf("two runs learned different merges:\n%q\n%q", a.merges, b.merges)
	}
}

func TestTrainStops(t *testing.T) {
	// " b" and " c" merge, and then every chunk is a single token, so
	// training stops early with 2 merges, not 10.
	tok := Train("a b c", 10)
	if want := []pair{{" ", "b"}, {" ", "c"}}; !slices.Equal(tok.merges, want) {
		t.Errorf("merges = %q, want %q", tok.merges, want)
	}
}
