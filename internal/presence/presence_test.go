package presence

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

func TestParsers(t *testing.T) {
	if d, err := parseIoreg(`  | |   "HIDIdleTime" = 5000000000`); err != nil || d != 5*time.Second {
		t.Fatal(d, err)
	}
	if _, err := parseIoreg("nothing"); err == nil {
		t.Fatal("ioreg")
	}
	if d, err := parseMillis("1500\n"); err != nil || d != 1500*time.Millisecond {
		t.Fatal(d, err)
	}
	if _, err := parseMillis("x"); err == nil {
		t.Fatal("millis")
	}
	if d, err := parseGdbus("(uint64 2000,)"); err != nil || d != 2*time.Second {
		t.Fatal(d, err)
	}
	if _, err := parseGdbus("()"); err == nil {
		t.Fatal("gdbus")
	}
}

func TestWatcherTransitions(t *testing.T) {
	now := time.Unix(10_000, 0)
	idle := []time.Duration{time.Second, 3 * time.Minute, 4 * time.Minute, time.Second, 5 * time.Minute}
	var i int
	var got []event.Event
	w := &Watcher{
		Idle:      func() (time.Duration, error) { d := idle[i]; i++; return d, nil },
		Threshold: 2 * time.Minute,
		Now:       func() time.Time { return now },
		Emit: func(e event.Event) {
			if e.Kind == event.Idle {
				got = append(got, e)
			}
		},
	}
	for range idle {
		w.Tick()
	}
	if len(got) != 2 {
		t.Fatalf("want 2 idle events (one per away period), got %d", len(got))
	}
	if !got[0].Time.Equal(now.Add(-3*time.Minute)) || got[0].Kind != event.Idle {
		t.Fatalf("idle should be backdated to last input: %+v", got[0])
	}
	if err := got[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherErrorEmitsNothing(t *testing.T) {
	w := &Watcher{Idle: func() (time.Duration, error) { return 0, errors.New("x") }, Threshold: time.Second,
		Now: time.Now, Emit: func(event.Event) { t.Fatal("emitted") }}
	w.Tick()
}

func TestRunStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	w := &Watcher{Idle: func() (time.Duration, error) { n++; return 0, nil }, Threshold: time.Hour,
		Interval: 5 * time.Millisecond, Now: time.Now, Emit: func(event.Event) {}}
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if n == 0 {
		t.Fatal("never ticked")
	}
}

// Real OS backend: macOS and Windows always have an idle counter; Linux CI may be headless.
func TestSystemIdleReal(t *testing.T) {
	d, err := System()
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if d < 0 || d > 30*24*time.Hour {
		t.Fatalf("implausible idle %v", d)
	}
}

// On a real display, synthetic input must reset the OS idle counter.
// VIBETIME_PRESENCE_INPUT is a shell command that moves the mouse (CI: xdotool under Xvfb).
func TestSystemIdleResetsOnInput(t *testing.T) {
	cmd := os.Getenv("VIBETIME_PRESENCE_INPUT")
	if cmd == "" {
		t.Skip("set VIBETIME_PRESENCE_INPUT")
	}
	time.Sleep(2500 * time.Millisecond)
	before, err := System()
	if err != nil || before < 2*time.Second {
		t.Fatalf("idle should have grown: %v %v", before, err)
	}
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	after, err := System()
	if err != nil || after >= before || after > time.Second {
		t.Fatalf("input did not reset idle: before %v after %v (%v)", before, after, err)
	}
}

func TestWatcherEmitsActiveOncePerMinute(t *testing.T) {
	now := time.Unix(60_000, 0)
	var got []event.Kind
	idle := 5 * time.Second
	w := &Watcher{Idle: func() (time.Duration, error) { return idle, nil }, Threshold: 2 * time.Minute,
		Now: func() time.Time { return now }, Emit: func(e event.Event) { got = append(got, e.Kind) }}
	for i := 0; i < 8; i++ { // 15s ticks over 2 minutes
		w.Tick()
		now = now.Add(15 * time.Second)
	}
	if len(got) != 2 || got[0] != event.Active {
		t.Fatalf("%v", got)
	}
	idle = 90 * time.Second // not idle yet, but no recent input: no Active
	now = now.Add(time.Minute)
	w.Tick()
	if len(got) != 2 {
		t.Fatalf("active without recent input: %v", got)
	}
}
