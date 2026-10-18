package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"call the dentist", 20, "call the dentist"},
		{"call the dentist", 16, "call the dentist"},
		{"call the dentist", 15, "call the denti…"},
		{"call the dentist", 9, "call the…"},
		{"call the dentist", 10, "call the…"},
		{"call the dentist", 1, "…"},
		{"call the dentist", 0, ""},
		{"call the dentist", -3, ""},
		{"", 5, ""},
		{"café com açúcar", 6, "café…"},
		{"明天下午三点开会", 5, "明天下午…"},
		{"🦷🦷🦷🦷", 4, "🦷🦷🦷🦷"},
		{"🦷🦷🦷🦷", 3, "🦷🦷…"},
	}
	for _, tt := range tests {
		got := Truncate(tt.s, tt.n)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
		if n := utf8.RuneCountInString(got); tt.n > 0 && n > tt.n {
			t.Errorf("Truncate(%q, %d) has %d runes", tt.s, tt.n, n)
		}
	}
}

func TestNormalizeSpace(t *testing.T) {
	tests := []struct{ s, want string }{
		{"buy milk", "buy milk"},
		{"  buy \t milk\n\n now  ", "buy milk now"},
		{"pão de queijo", "pão de queijo"},
		{"明天 \n 开会", "明天 开会"},
		{" \t\n", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeSpace(tt.s); got != tt.want {
			t.Errorf("NormalizeSpace(%q) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ s, want string }{
		{"Call the Dentist", "call-the-dentist"},
		{"Café com Ana!", "cafe-com-ana"},
		{"  Größe & Maße  ", "grosse-masse"},
		{"Ærøskøbing Łódź", "aeroskobing-lodz"},
		{"Ação--Reunião (2026)", "acao-reuniao-2026"},
		{"Café", "cafe"},
		{"明天 meeting", "meeting"},
		{"!!!", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Slug(tt.s); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct{ s, want string }{
		{"one line", "one line"},
		{"subject\nbody text", "subject"},
		{"subject\r\nbody", "subject"},
		{"\n\n  \n  pagar a conta  \nmais", "pagar a conta"},
		{"明天开会\n下午三点", "明天开会"},
		{"no newline at end\n", "no newline at end"},
		{"\n \n", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := FirstLine(tt.s); got != tt.want {
			t.Errorf("FirstLine(%q) = %q, want %q", tt.s, got, tt.want)
		}
	}
}
