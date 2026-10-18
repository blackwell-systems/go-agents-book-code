package i18n

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Match picks the supported language that best fits an Accept-Language
// header such as "pt-BR,pt;q=0.9,en;q=0.5". Ranges are tried in order of
// q-value; a range matches a supported language exactly or by primary
// subtag, so "pt-BR" matches "pt". Malformed entries are skipped, and q=0
// excludes a language from "*". With no match, Match returns the first
// supported language, or "" if there are none.
func Match(acceptLanguage string, supported []string) string {
	if len(supported) == 0 {
		return ""
	}
	type weighted struct {
		tag string
		q   float64
	}
	var prefs []weighted
	refused := make(map[string]bool)
	for entry := range strings.SplitSeq(acceptLanguage, ",") {
		tag, q, ok := parseRange(entry)
		if !ok {
			continue
		}
		if q == 0 {
			refused[tag] = true
			continue
		}
		prefs = append(prefs, weighted{tag, q})
	}
	slices.SortStableFunc(prefs, func(a, b weighted) int { return cmp.Compare(b.q, a.q) })

	for _, p := range prefs {
		if s, ok := lookup(p.tag, supported, refused); ok {
			return s
		}
	}
	return supported[0]
}

// lookup finds the supported language for one language range.
func lookup(tag string, supported []string, refused map[string]bool) (string, bool) {
	if tag == "*" {
		for _, s := range supported {
			if !refused[strings.ToLower(s)] {
				return s, true
			}
		}
		return "", false
	}
	for _, s := range supported {
		if strings.EqualFold(s, tag) {
			return s, true
		}
	}
	for _, s := range supported {
		if base(s) == base(tag) {
			return s, true
		}
	}
	return "", false
}

// parseRange parses one Accept-Language entry, such as "de-AT;q=0.8",
// into a lowercase tag and its q-value.
func parseRange(entry string) (tag string, q float64, ok bool) {
	tag, params, _ := strings.Cut(entry, ";")
	tag = strings.ToLower(strings.TrimSpace(tag))
	if !validTag(tag) {
		return "", 0, false
	}
	q = 1
	for p := range strings.SplitSeq(params, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		k, v, found := strings.Cut(p, "=")
		if !found {
			return "", 0, false
		}
		if strings.TrimSpace(k) != "q" {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 || f > 1 {
			return "", 0, false
		}
		q = f
	}
	return tag, q, true
}

// validTag reports whether tag is "*" or a language tag made of
// alphanumeric subtags of one to eight characters, the first all
// letters.
func validTag(tag string) bool {
	if tag == "*" {
		return true
	}
	for i, sub := range strings.Split(tag, "-") {
		if len(sub) == 0 || len(sub) > 8 {
			return false
		}
		for _, r := range sub {
			letter := r >= 'a' && r <= 'z'
			digit := r >= '0' && r <= '9'
			if !letter && (i == 0 || !digit) {
				return false
			}
		}
	}
	return true
}
