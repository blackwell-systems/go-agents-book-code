// Package textutil has small helpers for reminder text: shortening it,
// tidying whitespace, and making URL slugs.
package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ellipsis marks text that Truncate shortened.
const ellipsis = "…"

// Truncate shortens s to at most n runes. Shortened text ends in "…",
// which counts toward n.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n-1 {
			return strings.TrimRightFunc(s[:pos], unicode.IsSpace) + ellipsis
		}
		i++
	}
	return s
}

// NormalizeSpace trims s and replaces each run of whitespace inside it
// with a single space.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// FirstLine returns the first line of s that isn't blank, trimmed of
// surrounding whitespace.
func FirstLine(s string) string {
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// latin maps accented Latin letters to ASCII. Each entry is the ASCII
// form followed by the lowercase letters that become it.
var latin = func() map[rune]string {
	table := []string{
		"a àáâãäåāăą",
		"ae æ",
		"c çćčĉċ",
		"d ďđð",
		"e èéêëēĕėęě",
		"g ĝğġģ",
		"h ĥħ",
		"i ìíîïĩīĭįı",
		"j ĵ",
		"k ķ",
		"l ĺļľŀł",
		"n ñńņňŉ",
		"o òóôõöøōŏő",
		"oe œ",
		"r ŕŗř",
		"s śŝşšș",
		"ss ß",
		"t ţťŧț",
		"th þ",
		"u ùúûüũūŭůűų",
		"w ŵ",
		"y ýÿŷ",
		"z źżž",
	}
	m := make(map[rune]string)
	for _, row := range table {
		ascii, letters, _ := strings.Cut(row, " ")
		for _, r := range letters {
			m[r] = ascii
		}
	}
	return m
}()

// Slug turns s into lowercase ASCII words joined by hyphens, for use in
// URLs and file names: "Café com Ana!" becomes "cafe-com-ana". Accented
// Latin letters become plain ones; other characters separate words.
func Slug(s string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(s) {
		var word string
		switch {
		case 'a' <= r && r <= 'z', '0' <= r && r <= '9':
			word = string(r)
		case latin[r] != "":
			word = latin[r]
		case unicode.IsMark(r):
			continue
		default:
			hyphen = true
			continue
		}
		if hyphen && b.Len() > 0 {
			b.WriteByte('-')
		}
		hyphen = false
		b.WriteString(word)
	}
	return b.String()
}
