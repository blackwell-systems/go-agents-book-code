package search

import (
	"strings"
	"unicode"
)

// stopwords are common English, Portuguese, and German words that
// carry little meaning in a search.
var stopwords = func() map[string]bool {
	const words = `
		a an and are as at be by for from in is it of on or the to with
		o os as da das de do dos e em na nas no nos para por que um uma com
		der die das den dem des ein eine einen und ist im zu mit von auf für nicht
	`
	m := make(map[string]bool)
	for w := range strings.FieldsSeq(words) {
		m[w] = true
	}
	return m
}()

// tokenize splits text into lowercase terms: runs of letters and digits,
// with each Chinese, Japanese, or Korean character as its own term.
// Stopwords are dropped.
func tokenize(text string) []string {
	var terms []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			if w := string(word); !stopwords[w] {
				terms = append(terms, w)
			}
			word = word[:0]
		}
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			flush()
			terms = append(terms, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, unicode.ToLower(r))
		case unicode.IsMark(r) && len(word) > 0:
			word = append(word, r)
		default:
			flush()
		}
	}
	flush()
	return terms
}

// isCJK reports whether r is written without spaces between words.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
