package i18n

// catalogs maps a language to its messages by key.
var catalogs = map[string]map[string]message{
	"en": {
		"reminder.subject": {other: "Reminder: {text}"},
		"reminder.body":    {other: "Hi {name}, here is your reminder: {text}"},
		"reminder.footer":  {other: "You can change how you get reminders in your settings."},
		"digest.title": {
			one:   "{name}, you have {count} reminder coming up",
			other: "{name}, you have {count} reminders coming up",
		},
		"digest.empty": {
			one:   "Nothing is scheduled for the next day.",
			other: "Nothing is scheduled for the next {count} days.",
		},
		"snooze.confirm":  {other: "Snoozed until {time}."},
		"time.in.minutes": {one: "in {count} minute", other: "in {count} minutes"},
		"time.in.hours":   {one: "in {count} hour", other: "in {count} hours"},
		"time.in.days":    {one: "in {count} day", other: "in {count} days"},
	},
	"pt": {
		"reminder.subject": {other: "Lembrete: {text}"},
		"reminder.body":    {other: "Olá {name}, aqui está o seu lembrete: {text}"},
		"digest.title": {
			one:   "{name}, você tem {count} lembrete pela frente",
			other: "{name}, você tem {count} lembretes pela frente",
		},
		"digest.empty": {
			one:   "Nada agendado para o próximo dia.",
			other: "Nada agendado para os próximos {count} dias.",
		},
		"snooze.confirm":  {other: "Adiado até {time}."},
		"time.in.minutes": {one: "em {count} minuto", other: "em {count} minutos"},
		"time.in.hours":   {one: "em {count} hora", other: "em {count} horas"},
		"time.in.days":    {one: "em {count} dia", other: "em {count} dias"},
	},
	"de": {
		"reminder.subject": {other: "Erinnerung: {text}"},
		"reminder.body":    {other: "Hallo {name}, hier ist deine Erinnerung: {text}"},
		"reminder.footer":  {other: "Wie du erinnert wirst, kannst du in den Einstellungen ändern."},
		"digest.title": {
			one:   "{name}, du hast {count} anstehende Erinnerung",
			other: "{name}, du hast {count} anstehende Erinnerungen",
		},
		"digest.empty": {
			one:   "Für den nächsten Tag ist nichts geplant.",
			other: "Für die nächsten {count} Tage ist nichts geplant.",
		},
		"snooze.confirm":  {other: "Verschoben bis {time}."},
		"time.in.minutes": {one: "in {count} Minute", other: "in {count} Minuten"},
		"time.in.hours":   {one: "in {count} Stunde", other: "in {count} Stunden"},
		"time.in.days":    {one: "in {count} Tag", other: "in {count} Tagen"},
	},
	"ja": {
		"reminder.subject": {other: "リマインダー: {text}"},
		"reminder.body":    {other: "{name}さん、リマインダーです: {text}"},
		"digest.title":     {other: "{name}さん、予定されているリマインダーは{count}件です"},
		"digest.empty":     {other: "今後{count}日間の予定はありません。"},
		"snooze.confirm":   {other: "{time}までスヌーズしました。"},
		"time.in.minutes":  {other: "{count}分後"},
		"time.in.hours":    {other: "{count}時間後"},
		"time.in.days":     {other: "{count}日後"},
	},
}
