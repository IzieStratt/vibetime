package presence

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hidRe = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// parseIoreg extracts HIDIdleTime (nanoseconds) from `ioreg -c IOHIDSystem` output.
func parseIoreg(out string) (time.Duration, error) {
	m := hidRe.FindStringSubmatch(out)
	if m == nil {
		return 0, errors.New("HIDIdleTime not found")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return time.Duration(n), err
}

// parseMillis parses xprintidle output (milliseconds).
func parseMillis(out string) (time.Duration, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	return time.Duration(n) * time.Millisecond, err
}

var gdbusRe = regexp.MustCompile(`uint64 (\d+)`)

// parseGdbus parses Mutter IdleMonitor output like "(uint64 1234,)" (milliseconds).
func parseGdbus(out string) (time.Duration, error) {
	m := gdbusRe.FindStringSubmatch(out)
	if m == nil {
		return 0, errors.New("no idletime in gdbus output")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return time.Duration(n) * time.Millisecond, err
}
