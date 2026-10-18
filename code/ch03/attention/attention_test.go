package attention

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestCosine(t *testing.T) {
	tests := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{2, 0}, 1},
		{[]float64{1, 0}, []float64{0, 3}, 0},
		{[]float64{1, 1}, []float64{-1, -1}, -1},
		{[]float64{1, 0}, []float64{1, 1}, 1 / math.Sqrt2},
	}
	for _, tt := range tests {
		if got := Cosine(tt.a, tt.b); !near(got, tt.want) {
			t.Errorf("Cosine(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSoftmax(t *testing.T) {
	// e^0 : e^ln3 = 1 : 3
	got := Softmax([]float64{0, math.Log(3)})
	if !near(got[0], 0.25) || !near(got[1], 0.75) {
		t.Errorf("Softmax = %v, want [0.25 0.75]", got)
	}
	// Huge scores would overflow math.Exp without subtracting the top.
	got = Softmax([]float64{1000, 1000})
	if !near(got[0], 0.5) || !near(got[1], 0.5) {
		t.Errorf("Softmax(1000, 1000) = %v, want [0.5 0.5]", got)
	}
	if !math.IsInf(math.Exp(1000), 1) {
		t.Error("math.Exp(1000) no longer overflows; update the Go note")
	}
}

func TestSoftmaxMask(t *testing.T) {
	got := Softmax([]float64{1, math.Inf(-1), 1})
	if !near(got[0], 0.5) || got[1] != 0 || !near(got[2], 0.5) {
		t.Errorf("Softmax with a masked score = %v", got)
	}
}

func TestTemperature(t *testing.T) {
	scores := []float64{2, 1}
	hot := Softmax(WithTemperature(scores, 2))
	cold := Softmax(WithTemperature(scores, 0.5))
	plain := Softmax(scores)
	if !(cold[0] > plain[0] && plain[0] > hot[0]) {
		t.Errorf("top probability: cold %v, plain %v, hot %v", cold[0],
			plain[0], hot[0])
	}
}

func TestAttend(t *testing.T) {
	// The query matches the first key exactly and is unrelated to the
	// second: scores 2/√2 and 0, weights 0.8044 and 0.1956.
	q := []float64{1, 1}
	keys := [][]float64{{1, 1}, {1, -1}}
	values := [][]float64{{10, 0}, {0, 10}}
	w, out := Attend(q, keys, values)
	if !near(w[0], 0.8044) || !near(w[1], 0.1956) {
		t.Errorf("weights = %v", w)
	}
	if !near(out[0], 8.044) || !near(out[1], 1.956) {
		t.Errorf("blend = %v", out)
	}
}
