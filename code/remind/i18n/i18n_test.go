package i18n

import (
	"testing"
	"time"
)

func TestTranslate(t *testing.T) {
	args := map[string]string{"name": "Ana", "text": "call mum", "time": "18:00"}
	tests := []struct {
		lang, key string
		n         int
		want      string
	}{
		{"en", "reminder.subject", 0, "Reminder: call mum"},
		{"pt", "reminder.body", 0, "Olá Ana, aqui está o seu lembrete: call mum"},
		{"de", "snooze.confirm", 0, "Verschoben bis 18:00."},
		{"ja", "reminder.body", 0, "Anaさん、リマインダーです: call mum"},
		{"DE", "reminder.subject", 0, "Erinnerung: call mum"},
		{"pt-BR", "snooze.confirm", 0, "Adiado até 18:00."},
		{"de_AT", "snooze.confirm", 0, "Verschoben bis 18:00."},

		// Fallbacks: missing key to English, unknown language to English,
		// unknown key to itself.
		{"pt", "reminder.footer", 0, "You can change how you get reminders in your settings."},
		{"de", "reminder.footer", 0, "Wie du erinnert wirst, kannst du in den Einstellungen ändern."},
		{"fr", "reminder.subject", 0, "Reminder: call mum"},
		{"", "snooze.confirm", 0, "Snoozed until 18:00."},
		{"de", "no.such.key", 0, "no.such.key"},
	}
	for _, tt := range tests {
		t.Run(tt.lang+"/"+tt.key, func(t *testing.T) {
			if got := Translate(tt.lang, tt.key, tt.n, args); got != tt.want {
				t.Errorf("Translate(%q, %q) = %q, want %q", tt.lang, tt.key, got, tt.want)
			}
		})
	}
}

func TestTranslatePlural(t *testing.T) {
	args := map[string]string{"name": "Kai"}
	tests := []struct {
		lang string
		n    int
		want string
	}{
		{"en", 0, "Kai, you have 0 reminders coming up"},
		{"en", 1, "Kai, you have 1 reminder coming up"},
		{"en", 2, "Kai, you have 2 reminders coming up"},
		{"pt", 0, "Kai, você tem 0 lembrete pela frente"},
		{"pt", 1, "Kai, você tem 1 lembrete pela frente"},
		{"pt", 5, "Kai, você tem 5 lembretes pela frente"},
		{"de", 1, "Kai, du hast 1 anstehende Erinnerung"},
		{"de", 3, "Kai, du hast 3 anstehende Erinnerungen"},
		{"ja", 1, "Kaiさん、予定されているリマインダーは1件です"},
		{"ja", 4, "Kaiさん、予定されているリマインダーは4件です"},
	}
	for _, tt := range tests {
		if got := Translate(tt.lang, "digest.title", tt.n, args); got != tt.want {
			t.Errorf("Translate(%q, digest.title, %d) = %q, want %q", tt.lang, tt.n, got, tt.want)
		}
	}
}

func TestTranslateArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]string
		want string
	}{
		{"nil args", nil, "Nothing is scheduled for the next 7 days."},
		{"count from args", map[string]string{"count": "seven"}, "Nothing is scheduled for the next seven days."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Translate("en", "digest.empty", 7, tt.args); got != tt.want {
				t.Errorf("Translate = %q, want %q", got, tt.want)
			}
		})
	}
	if got := Translate("en", "reminder.body", 0, nil); got != "Hi {name}, here is your reminder: {text}" {
		t.Errorf("missing args: got %q, want placeholders left in place", got)
	}
}

func TestMatch(t *testing.T) {
	supported := Languages()
	tests := []struct {
		header string
		want   string
	}{
		{"", "en"},
		{"de", "de"},
		{"pt-BR", "pt"},
		{"PT-br,en;q=0.5", "pt"},
		{"fr-CA,fr;q=0.9,de;q=0.8", "de"},
		{"en;q=0.2,ja;q=0.9", "ja"},
		{"ja;q=0.5,de;q=0.5", "ja"},
		{"*", "en"},
		{"fr,*;q=0.1", "en"},
		{"en;q=0,*", "pt"},
		{"de;q=0", "en"},
		{"de;q=0,ja;q=0.1", "ja"},
		{"de;q=abc,ja", "ja"},
		{"de;q=1.5,ja;q=0.3", "ja"},
		{"de;q", "en"},
		{";q=0.5,  ,de", "de"},
		{"de!,pt", "pt"},
		{"toolongsubtag,ja", "ja"},
		{"12,ja", "ja"},
		{"zh-Hant-TW", "en"},
		{"de-1996", "de"},
		{"ja ; q=0.8 , de ; q=0.7", "ja"},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			if got := Match(tt.header, supported); got != tt.want {
				t.Errorf("Match(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestMatchSupported(t *testing.T) {
	if got := Match("de", nil); got != "" {
		t.Errorf("Match with no supported languages = %q, want empty", got)
	}
	if got := Match("pt", []string{"en", "pt-BR"}); got != "pt-BR" {
		t.Errorf("Match(pt) = %q, want pt-BR", got)
	}
	if got := Match("en-GB,en-US;q=0.9", []string{"en-US", "en-GB"}); got != "en-GB" {
		t.Errorf("Match(en-GB) = %q, want en-GB", got)
	}
}

func TestFormatIn(t *testing.T) {
	tests := []struct {
		lang string
		d    time.Duration
		want string
	}{
		{"en", 10 * time.Second, "in 1 minute"},
		{"en", 90 * time.Second, "in 2 minutes"},
		{"en", 59*time.Minute + 40*time.Second, "in 1 hour"},
		{"en", 2*time.Hour + 10*time.Minute, "in 2 hours"},
		{"en", 23*time.Hour + 40*time.Minute, "in 1 day"},
		{"en", 60 * time.Hour, "in 3 days"},
		{"pt", time.Minute, "em 1 minuto"},
		{"pt", 2 * time.Hour, "em 2 horas"},
		{"pt", 48 * time.Hour, "em 2 dias"},
		{"de", 45 * time.Minute, "in 45 Minuten"},
		{"de", time.Hour, "in 1 Stunde"},
		{"de", 2 * time.Hour, "in 2 Stunden"},
		{"de", 24 * time.Hour, "in 1 Tag"},
		{"de", 72 * time.Hour, "in 3 Tagen"},
		{"ja", 5 * time.Minute, "5分後"},
		{"ja", 2 * time.Hour, "2時間後"},
		{"ja", 96 * time.Hour, "4日後"},
		{"fr", 2 * time.Hour, "in 2 hours"},
	}
	for _, tt := range tests {
		t.Run(tt.lang+"/"+tt.d.String(), func(t *testing.T) {
			if got := FormatIn(tt.lang, tt.d); got != tt.want {
				t.Errorf("FormatIn(%q, %v) = %q, want %q", tt.lang, tt.d, got, tt.want)
			}
		})
	}
}
