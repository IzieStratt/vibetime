package service

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPlans(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		p, err := For(goos, "/opt/vibe time/vibetime", "/home/kid")
		if err != nil || len(p.Commands) == 0 || len(p.Remove) == 0 {
			t.Fatalf("%s: %v", goos, err)
		}
		all := p.Content + strings.Join(append(p.Commands[0], p.Commands[len(p.Commands)-1]...), " ")
		if !strings.Contains(all, "vibe time/vibetime") || !strings.Contains(all, "serve") {
			t.Errorf("%s: exe/serve missing: %s", goos, all)
		}
	}
	if p, _ := For("windows", `C:\x\vibetime.exe`, ""); !strings.Contains(strings.Join(p.Commands[0], " "), "LIMITED") {
		t.Fatal("windows task must not need admin")
	}
	if p, _ := For("darwin", "/a&b<c", "/h"); !strings.Contains(p.Content, "/a&amp;b&lt;c") {
		t.Fatal("plist not escaped")
	}
	if _, err := For("plan9", "", ""); err == nil {
		t.Fatal("plan9")
	}
}

func TestApplyAndRemove(t *testing.T) {
	var ran [][]string
	saved := Runner
	defer func() { Runner = saved }()
	Runner = func(a []string) error {
		ran = append(ran, a)
		if a[1] == "unload" && len(a) == 3 {
			return errors.New("not loaded") // tolerated on install
		}
		return nil
	}
	dir := t.TempDir()
	p, _ := For("darwin", "/bin/vibetime", dir)
	if err := apply(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.File); err != nil || len(ran) != 2 {
		t.Fatal(err, ran)
	}
	if err := remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.File); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
	if err := remove(p); err != nil {
		t.Fatal("remove must be idempotent")
	}
	Runner = func([]string) error { return errors.New("boom") }
	lp, _ := For("linux", "/bin/vibetime", dir)
	if err := apply(lp); err == nil {
		t.Fatal("expected failure")
	}
	if err := remove(lp); err == nil {
		t.Fatal("expected failure")
	}
	f := filepath.Join(dir, "file")
	os.WriteFile(f, nil, 0o600)
	if err := apply(Plan{File: filepath.Join(f, "x", "y")}); err == nil {
		t.Fatal("unwritable")
	}
}

func TestRealRunner(t *testing.T) {
	if err := Runner([]string{"go", "version"}); err != nil {
		t.Fatal(err)
	}
	if err := Runner([]string{"go", "no-such-cmd"}); err == nil || !strings.Contains(err.Error(), "go no-such-cmd") {
		t.Fatal(err)
	}
}

// Real install on a disposable CI machine: VIBETIME_SERVICE_TEST=1 with a built binary in VIBETIME_BIN.
// Installs the login service, checks the agent comes up on its port, then uninstalls.
func TestRealInstall(t *testing.T) {
	bin := os.Getenv("VIBETIME_BIN")
	if os.Getenv("VIBETIME_SERVICE_TEST") == "" || bin == "" {
		t.Skip("set VIBETIME_SERVICE_TEST=1 and VIBETIME_BIN")
	}
	if runtime.GOOS == "linux" {
		if exec.Command("systemctl", "--user", "is-system-running").Run() != nil {
			if out, _ := exec.Command("systemctl", "--user", "is-system-running").Output(); len(out) == 0 {
				t.Skip("no systemd user session")
			}
		}
	}
	home, _ := os.UserHomeDir()
	p, err := For(runtime.GOOS, bin, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(p); err != nil {
		t.Fatal(err)
	}
	defer remove(p)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if c, err := net.Dial("tcp", "127.0.0.1:47391"); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not start from the login service")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := remove(p); err != nil {
		t.Fatal(err)
	}
}
