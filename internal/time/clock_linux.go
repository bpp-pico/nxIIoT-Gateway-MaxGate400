//go:build linux

package timeservice

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// osClock steps the kernel's wall-clock time via settimeofday(2) — the
// same syscall `date -s`/`hwclock --hctosys` use. Requires CAP_SYS_TIME;
// the gateway process runs as root under systemd (see
// deploy/nxiiot-gateway.service), which already grants it.
type osClock struct{}

func newSystemClock() SystemClock { return osClock{} }

func (osClock) Set(t time.Time) error {
	tv := unix.NsecToTimeval(t.UTC().UnixNano())
	if err := unix.Settimeofday(&tv); err != nil {
		return fmt.Errorf("settimeofday: %w", err)
	}
	return nil
}
