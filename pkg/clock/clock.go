package clock

import "time"

// Clock allows replacing time source in tests.
type Clock interface {
	Now() time.Time
}

// RealClock uses the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

// FixedClock always returns a fixed timestamp.
type FixedClock struct {
	Time time.Time
}

func (c FixedClock) Now() time.Time {
	return c.Time
}
