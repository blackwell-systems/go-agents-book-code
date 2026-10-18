package notify_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/remind/notify"
)

func TestHiddenPreviewMultibyte(t *testing.T) {
	short := strings.Repeat("予", 30) // 90 bytes, 30 runes
	if got := notify.Preview(short); got != short {
		t.Errorf("Preview(30 runes) = %q, want it unchanged", got)
	}
	long := strings.Repeat("予", 100)
	got := notify.Preview(long)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) > 61 || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Errorf("Preview(100 runes) = %q", got)
	}
}
