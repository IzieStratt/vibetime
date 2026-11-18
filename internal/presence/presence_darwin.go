package presence

import (
	"os/exec"
	"time"
)

func systemIdle() (time.Duration, error) {
	out, err := exec.Command("ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0, err
	}
	return parseIoreg(string(out))
}
