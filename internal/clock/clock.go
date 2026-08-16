// Package clock provides an injectable time source. Production code uses Wall
// (time.Now); tests use Virtual, a fixed UTC clock that advances only when the
// test explicitly moves it, so lease expiry and ordering are fully determined.
package clock

import (
	"sync"
	"time"
)

// Clock abstracts the current time so that deterministic tests can freeze it.
type Clock interface {
	// Now returns the current time in UTC.
	Now() time.Time
}

// Wall is the real-time clock.
type Wall struct{}

// Now returns time.Now in UTC.
func (Wall) Now() time.Time { return time.Now().UTC() }

// Virtual is a controllable UTC clock for tests. It is safe for concurrent use.
type Virtual struct {
	mu sync.Mutex
	t  time.Time
}

// NewVirtual returns a virtual clock pinned to t (converted to UTC).
func NewVirtual(t time.Time) *Virtual {
	return &Virtual{t: t.UTC()}
}

// Now returns the clock's current time.
func (v *Virtual) Now() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.t
}

// Set moves the clock to t (UTC).
func (v *Virtual) Set(t time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.t = t.UTC()
}

// Advance moves the clock forward by d.
func (v *Virtual) Advance(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.t = v.t.Add(d)
}

// ISO returns the current time as an ISO-8601 UTC string suitable for storage.
func (v *Virtual) ISO() string {
	return v.Now().Format("2006-01-02T15:04:05.000000Z")
}

// ISOFrom returns the ISO-8601 UTC string for a clock reading.
func ISOFrom(c Clock) string {
	return c.Now().Format("2006-01-02T15:04:05.000000Z")
}
