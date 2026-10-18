package schedule_test

import (
	"strings"
	"testing"
	"time"

	"example.com/remind/schedule"
)

func TestHiddenUnknownDay(t *testing.T) {
	for _, spec := range []string{"on funday at 09:00", "on mon,thurs at 08:00", "on mon,\nNOTE at 09:00"} {
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("Parse(%q) panicked", spec)
				}
			}()
			if _, err := schedule.Parse(spec, schedule.UTC, time.Now()); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", spec)
			}
		}()
	}
	r, err := schedule.Parse("on Monday, wed at 09:00", schedule.UTC, time.Now())
	if err != nil || !strings.HasPrefix(r.String(), "on mon,wed") {
		t.Errorf("Parse(on Monday, wed at 09:00) = %v, %v", r, err)
	}
}
