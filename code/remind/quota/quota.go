// Package quota holds account plans, reminder quotas, and per-user
// request rate limits.
package quota

import (
	"errors"
	"fmt"
)

// ErrQuotaExceeded reports that an account is at its plan's limit.
var ErrQuotaExceeded = errors.New("quota exceeded")

// ErrUnknownPlan reports a plan name that matches no plan.
var ErrUnknownPlan = errors.New("unknown plan")

// A Plan sets what an account may store and how fast it may call the API.
type Plan struct {
	Name         string
	MaxReminders int     // reminders an account may hold at once
	Rate         float64 // requests per second, sustained
	Burst        int     // requests allowed at once after a quiet spell
}

// The plans remind offers.
var (
	Free = Plan{Name: "free", MaxReminders: 50, Rate: 5, Burst: 20}
	Pro  = Plan{Name: "pro", MaxReminders: 5000, Rate: 50, Burst: 100}
)

// PlanByName returns the plan called name ("free" or "pro").
func PlanByName(name string) (Plan, error) {
	switch name {
	case Free.Name:
		return Free, nil
	case Pro.Name:
		return Pro, nil
	}
	return Plan{}, fmt.Errorf("%w: %q", ErrUnknownPlan, name)
}

// CheckReminders reports whether an account on plan p that holds count
// reminders may create another. The error wraps ErrQuotaExceeded.
func CheckReminders(p Plan, count int) error {
	if count >= p.MaxReminders {
		return fmt.Errorf("%s plan allows %d reminders: %w", p.Name, p.MaxReminders, ErrQuotaExceeded)
	}
	return nil
}
