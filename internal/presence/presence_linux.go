package presence

import (
	"errors"
	"os/exec"
	"time"
)

// Tries X11 (xprintidle) then GNOME/Wayland (Mutter IdleMonitor over D-Bus).
func systemIdle() (time.Duration, error) {
	if out, err := exec.Command("xprintidle").Output(); err == nil {
		return parseMillis(string(out))
	}
	if out, err := exec.Command("gdbus", "call", "--session", "--dest", "org.gnome.Mutter.IdleMonitor",
		"--object-path", "/org/gnome/Mutter/IdleMonitor/Core", "--method", "org.gnome.Mutter.IdleMonitor.GetIdletime").Output(); err == nil {
		return parseGdbus(string(out))
	}
	return 0, errors.New("no idle source (install xprintidle or use GNOME)")
}
