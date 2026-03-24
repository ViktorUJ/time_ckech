package scheduler

import "time"

// Clock abstracts time retrieval for testability.
type Clock interface {
	Now() time.Time
}

// RealClock implements Clock using the real system time.
type RealClock struct{}

// Now returns the current system time.
func (RealClock) Now() time.Time {
	return time.Now()
}
