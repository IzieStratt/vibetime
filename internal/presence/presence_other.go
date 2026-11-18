//go:build !darwin && !windows && !linux

package presence

import (
	"errors"
	"time"
)

func systemIdle() (time.Duration, error) { return 0, errors.New("unsupported OS") }
