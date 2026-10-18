package toy

import (
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

const text = "the cat sat on the mat . the cat sat on the hat . " +
	"the cat ran to the mat ."

func TestNext(t *testing.T) {
	m := Train(text)
	got := m.Next("cat", "sat", 1)
	if len(got) != 1 || got[0] != (Choice{"on", 1}) {
		t.Errorf("Next(cat, sat) = %v, want [{on 1}]", got)
	}
	// "on the" is followed by mat once and hat once.
	got = m.Next("on", "the", 1)
	want := []Choice{{"hat", 0.5}, {"mat", 0.5}}
	if !slices.Equal(got, want) {
		t.Errorf("Next(on, the) = %v, want %v", got, want)
	}
	// "the cat" is followed by sat twice and ran once.
	for _, tt := range []struct {
		temperature float64
		sat         float64 // the probability of "sat"
	}{
		{1, 2.0 / 3},
		{0.5, 4.0 / 5},     // 2² against 1²
		{2, 1.414 / 2.414}, // √2 against √1
	} {
		got := m.Next("the", "cat", tt.temperature)
		if got[0].Word != "sat" || math.Abs(got[0].P-tt.sat) > 0.001 {
			t.Errorf("temperature %v: P(sat) = %v, want %.3f",
				tt.temperature, got[0], tt.sat)
		}
	}
	// A small temperature must not overflow: "sat" takes it all.
	if got := m.Next("the", "cat", 0.01); math.Abs(got[0].P-1) > 0.001 {
		t.Errorf("temperature 0.01: %v", got)
	}
	if got := m.Next("no", "such", 1); got != nil {
		t.Errorf("Next(no, such) = %v, want nil", got)
	}
}

func TestGenerate(t *testing.T) {
	m := Train(text)
	got := strings.Join(m.Generate("the", "cat", 5, 0, nil), " ")
	if want := "the cat sat on the hat ."; got != want {
		t.Errorf("greedy = %q, want %q", got, want)
	}
	// Sampling with the same seed gives the same words.
	a := m.Generate("the", "cat", 20, 1, rand.New(rand.NewPCG(1, 2)))
	b := m.Generate("the", "cat", 20, 1, rand.New(rand.NewPCG(1, 2)))
	if !slices.Equal(a, b) {
		t.Errorf("same seed, different words:\n%v\n%v", a, b)
	}
}

func TestSample(t *testing.T) {
	cs := []Choice{{"a", 0.5}, {"b", 0.3}, {"c", 0.2}}
	for _, tt := range []struct {
		r    float64
		want string
	}{{0, "a"}, {0.49, "a"}, {0.5, "b"}, {0.79, "b"}, {0.8, "c"}, {0.999, "c"}} {
		if got := sample(cs, tt.r); got != tt.want {
			t.Errorf("sample(%v) = %s, want %s", tt.r, got, tt.want)
		}
	}
}
