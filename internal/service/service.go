// Package service registers `vibetime serve` to start at login, per user, with no admin
// rights: launchd on macOS, systemd --user on Linux, Task Scheduler on Windows.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const Label = "com.vibetime.agent"

// Plan is what install does on one OS: write File (if any) then run Commands.
type Plan struct {
	File     string
	Content  string
	Commands [][]string
	Remove   [][]string // uninstall commands (run before deleting File)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// For returns the install plan for goos. exe is the absolute vibetime binary path.
func For(goos, exe, home string) (Plan, error) {
	switch goos {
	case "darwin":
		f := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
		log := filepath.Join(home, "Library", "Logs", "vibetime.log")
		return Plan{
			File: f,
			Content: `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key><array><string>` + xmlEscape(exe) + `</string><string>serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(log) + `</string>
</dict></plist>
`,
			Commands: [][]string{{"launchctl", "unload", f}, {"launchctl", "load", "-w", f}},
			Remove:   [][]string{{"launchctl", "unload", "-w", f}},
		}, nil
	case "linux":
		f := filepath.Join(home, ".config", "systemd", "user", "vibetime.service")
		return Plan{
			File: f,
			Content: `[Unit]
Description=vibetime AI time tracker

[Service]
ExecStart="` + exe + `" serve
Restart=on-failure
Nice=10

[Install]
WantedBy=default.target
`,
			Commands: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "vibetime.service"}},
			Remove:   [][]string{{"systemctl", "--user", "disable", "--now", "vibetime.service"}},
		}, nil
	case "windows":
		// No file: a per-user logon task. /RL LIMITED = no admin; /F overwrites.
		return Plan{
			Commands: [][]string{
				{"schtasks", "/Create", "/F", "/SC", "ONLOGON", "/RL", "LIMITED", "/TN", "vibetime", "/TR", `"` + exe + `" serve`},
				{"schtasks", "/Run", "/TN", "vibetime"},
			},
			Remove: [][]string{{"schtasks", "/End", "/TN", "vibetime"}, {"schtasks", "/Delete", "/F", "/TN", "vibetime"}},
		}, nil
	}
	return Plan{}, fmt.Errorf("unsupported OS %q", goos)
}

// Runner executes a command; replaced in tests.
var Runner = func(args []string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func current() (Plan, error) {
	exe, err := os.Executable()
	if err != nil {
		return Plan{}, err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Plan{}, err
	}
	return For(runtime.GOOS, exe, home)
}

func Install() error {
	p, err := current()
	if err != nil {
		return err
	}
	return apply(p)
}

func apply(p Plan) error {
	if p.File != "" {
		if err := os.MkdirAll(filepath.Dir(p.File), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p.File, []byte(p.Content), 0o644); err != nil {
			return err
		}
	}
	for i, c := range p.Commands {
		// launchctl unload of a not-yet-loaded job fails harmlessly
		if err := Runner(c); err != nil && !(i == 0 && len(p.Commands) > 1 && c[0] == "launchctl") {
			return err
		}
	}
	return nil
}

func Uninstall() error {
	p, err := current()
	if err != nil {
		return err
	}
	return remove(p)
}

func remove(p Plan) error {
	var first error
	for _, c := range p.Remove {
		if err := Runner(c); err != nil && first == nil {
			first = err
		}
	}
	if p.File != "" {
		if err := os.Remove(p.File); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return first
}
