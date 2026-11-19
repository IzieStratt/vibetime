// Package presence detects whether the user is at the computer. It only reads the
// system's "seconds since last input" counter — never keystrokes or screen content.
package presence

import (
	"context"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

// IdleFunc returns time since the last keyboard/mouse input.
type IdleFunc func() (time.Duration, error)

// System is the platform implementation (see presence_<os>.go).
var System IdleFunc = systemIdle

// Watcher emits an Idle event when the user crosses Threshold of inactivity.
type Watcher struct {
	Idle       IdleFunc
	Threshold  time.Duration
	Interval   time.Duration
	Now        func() time.Time
	Emit       func(event.Event)
	away       bool
	lastActive time.Time
}

// Tick checks once; exported for tests.
func (w *Watcher) Tick() {
	d, err := w.Idle()
	if err != nil {
		return // unknown: don't emit anything
	}
	if d >= w.Threshold {
		if !w.away {
			w.away = true
			w.Emit(event.Event{Time: w.Now().Add(-d), Kind: event.Idle})
		}
		return
	}
	w.away = false
	// Present: record one Active event per minute, so attended time can be checked against
	// real input. Recent input (d < 1 min) is required, not merely "not yet idle".
	if d < time.Minute {
		if m := w.Now().Truncate(time.Minute); !m.Equal(w.lastActive) {
			w.lastActive = m
			w.Emit(event.Event{Time: w.Now(), Kind: event.Active})
		}
	}
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick()
		}
	}
}
