package timeservice

import "time"

// SystemClock abstracts stepping the OS's live wall-clock time. Real
// implementation only exists on Linux (see clock_linux.go), where the
// gateway process runs as root under systemd and so already holds
// CAP_SYS_TIME — see clock_other.go for every other platform (Windows dev
// host, darwin, ...).
type SystemClock interface {
	Set(t time.Time) error
}
