//go:build !linux

package timeservice

import (
	"errors"
	"time"
)

// errSystemClockUnsupported is returned on every non-Linux build (Windows
// dev host, darwin, ...) where there is no portable way to step the OS
// wall clock. Callers must treat this the same as any other sync-side
// side effect failing — logged, never fatal (Rule 10).
var errSystemClockUnsupported = errors.New("timeservice: setting the system clock is not supported on this platform")

type unsupportedClock struct{}

func newSystemClock() SystemClock { return unsupportedClock{} }

func (unsupportedClock) Set(time.Time) error { return errSystemClockUnsupported }
