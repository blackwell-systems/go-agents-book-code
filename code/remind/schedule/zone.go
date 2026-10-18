package schedule

import "time"

// A Zone is a named time zone.
type Zone struct {
	Name string
	loc  *time.Location
}

// UTC is the zone used when a user hasn't chosen one.
var UTC = &Zone{Name: "UTC", loc: time.UTC}

// LookupZone returns the zone with the given IANA name, such as
// "Europe/Paris", or nil if there is no such zone. An empty name is UTC.
func LookupZone(name string) *Zone {
	if name == "" || name == "UTC" {
		return UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return &Zone{Name: name, loc: loc}
}

// Location returns the zone's location; a nil zone is UTC.
func (z *Zone) Location() *time.Location {
	if z == nil {
		return time.UTC
	}
	return z.loc
}
