// Package i18n translates notification text and formats relative times
// in the languages remind supports.
package i18n

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// A message holds the plural forms of one translated string. Languages
// without a singular form leave one empty.
type message struct {
	one   string
	other string
}

// languages lists the supported languages, the default first.
var languages = []string{"en", "pt", "de", "ja"}

// Languages returns the supported language codes, the default first.
func Languages() []string {
	return slices.Clone(languages)
}

// Translate returns the message for key in lang, choosing the plural
// form for n and replacing {placeholders} from args. {count} is n unless
// args sets it. A key missing in lang falls back to English, and a key
// missing there too is returned as is.
func Translate(lang, key string, n int, args map[string]string) string {
	lang = base(lang)
	m, ok := catalogs[lang][key]
	if !ok {
		lang = "en"
		if m, ok = catalogs[lang][key]; !ok {
			return key
		}
	}
	s := m.other
	if m.one != "" && isOne(lang, n) {
		s = m.one
	}
	return expand(s, n, args)
}

// isOne reports whether lang uses the singular form for n.
func isOne(lang string, n int) bool {
	switch lang {
	case "ja":
		return false
	case "pt":
		return n == 0 || n == 1
	default:
		return n == 1
	}
}

func expand(s string, n int, args map[string]string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	pairs := make([]string, 0, 2*len(args)+2)
	for k, v := range args {
		pairs = append(pairs, "{"+k+"}", v)
	}
	pairs = append(pairs, "{count}", strconv.Itoa(n))
	return strings.NewReplacer(pairs...).Replace(s)
}

// base returns the lowercase primary subtag of a language tag: "pt" for
// "pt-BR".
func base(tag string) string {
	tag = strings.ToLower(tag)
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// FormatIn describes a time d from now in lang, such as "in 2 hours".
// It uses whole minutes under an hour, whole hours under a day, and days
// beyond that, rounding to the nearest unit.
func FormatIn(lang string, d time.Duration) string {
	minutes := max(int(d.Round(time.Minute)/time.Minute), 1)
	if minutes < 60 {
		return Translate(lang, "time.in.minutes", minutes, nil)
	}
	hours := int(d.Round(time.Hour) / time.Hour)
	if hours < 24 {
		return Translate(lang, "time.in.hours", hours, nil)
	}
	const day = 24 * time.Hour
	return Translate(lang, "time.in.days", int(d.Round(day)/day), nil)
}
