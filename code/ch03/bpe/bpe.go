// Package bpe is a byte-pair encoding tokenizer: it learns a vocabulary
// from a text by repeatedly merging the most frequent pair of adjacent
// tokens, then splits new text into those tokens.
package bpe

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strconv"
)

// A pair is two adjacent tokens, a candidate for merging into one.
type pair struct{ a, b string }

// A Tokenizer splits text into tokens using the merges it learned.
type Tokenizer struct {
	merges []pair // in the order they were learned
}

// tag::chunks[]

// chunkRE splits text into the pieces that merges stay inside: a word
// or number with the space before it, a run of punctuation, or spaces.
var chunkRE = regexp.MustCompile(
	` ?[\p{L}\p{N}_]+| ?[^\s\p{L}\p{N}_]+|\s+`)

// byteTokens splits a chunk into one-byte tokens, the starting
// vocabulary.
func byteTokens(chunk string) []string {
	toks := make([]string, len(chunk))
	for i := range len(chunk) {
		toks[i] = chunk[i : i+1]
	}
	return toks
}

// end::chunks[]

// tag::train[]

// Train learns up to n merges from text.
func Train(text string, n int) *Tokenizer {
	counts := map[string]int{} // how often each chunk occurs
	for _, c := range chunkRE.FindAllString(text, -1) {
		counts[c]++
	}
	chunks := map[string][]string{} // each chunk, as tokens so far
	for c := range counts {
		chunks[c] = byteTokens(c)
	}
	t := &Tokenizer{}
	for range n {
		pairs := map[pair]int{}
		for c, toks := range chunks {
			for i := 0; i+1 < len(toks); i++ {
				pairs[pair{toks[i], toks[i+1]}] += counts[c] // <1>
			}
		}
		if len(pairs) == 0 {
			break // every chunk is a single token
		}
		best := mostFrequent(pairs)
		t.merges = append(t.merges, best) // <2>
		for c, toks := range chunks {
			chunks[c] = merge(toks, best)
		}
	}
	return t
}

// end::train[]

// mostFrequent returns the pair with the highest count, breaking ties
// by the merged text so training gives the same result every time.
func mostFrequent(pairs map[pair]int) pair {
	return slices.MaxFunc(slices.Collect(maps.Keys(pairs)),
		func(x, y pair) int {
			return cmp.Or(cmp.Compare(pairs[x], pairs[y]),
				cmp.Compare(y.a+y.b, x.a+x.b), cmp.Compare(y.a, x.a))
		})
}

// tag::merge[]

// merge replaces every adjacent p.a, p.b in toks with one token.
func merge(toks []string, p pair) []string {
	out := make([]string, 0, len(toks))
	for i := 0; i < len(toks); i++ {
		if i+1 < len(toks) && toks[i] == p.a && toks[i+1] == p.b {
			out = append(out, p.a+p.b)
			i++
			continue
		}
		out = append(out, toks[i])
	}
	return out
}

// Encode splits text into tokens by applying the merges, in the order
// they were learned, to each chunk. Real tokenizers instead apply the
// earliest-learned merge present, again and again, which is faster and
// almost always gives the same tokens.
func (t *Tokenizer) Encode(text string) []string {
	var out []string
	for _, c := range chunkRE.FindAllString(text, -1) {
		toks := byteTokens(c)
		for _, p := range t.merges {
			toks = merge(toks, p)
		}
		out = append(out, toks...)
	}
	return out
}

// end::merge[]

// Vocab returns the size of the vocabulary: the 256 single bytes and
// one token for each merge.
func (t *Tokenizer) Vocab() int {
	return 256 + len(t.merges)
}

// First returns the first n merged tokens, in the order learned.
func (t *Tokenizer) First(n int) []string {
	var out []string
	for _, p := range t.merges[:min(n, len(t.merges))] {
		out = append(out, strconv.Quote(p.a+p.b))
	}
	return out
}
