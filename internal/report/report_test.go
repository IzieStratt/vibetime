package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

var utc = time.UTC
var t0 = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

func TestEmpty(t *testing.T) {
	out := Render(nil, t0, 7, utc)
	if !strings.Contains(out, "No tracked time yet") || !strings.Contains(out, "last 7 days") {
		t.Fatal(out)
	}
}

func TestRender(t *testing.T) {
	var evs []event.Event
	// today: 10 min of heartbeats on cs2 (hands-on)
	for i := 0; i <= 10; i++ {
		evs = append(evs, event.Event{Time: t0.Add(time.Duration(i) * time.Minute), Kind: event.Heartbeat, Project: "cs2"})
	}
	// today: prompt then watch 4 min on site (steering + watching)
	evs = append(evs,
		event.Event{Time: t0.Add(time.Hour), Kind: event.Prompt, Project: "site"},
		event.Event{Time: t0.Add(time.Hour + 4*time.Minute), Kind: event.Heartbeat, Project: "site"})
	// 3 days ago: 20 min on cs2
	old := t0.AddDate(0, 0, -3)
	for i := 0; i <= 20; i++ {
		evs = append(evs, event.Event{Time: old.Add(time.Duration(i) * time.Minute), Kind: event.Heartbeat, Project: "cs2"})
	}
	out := Render(evs, t0, 7, utc)
	// today = 10 (cs2) + 4 (site) = 14m; total = 14 + 20 = 34m
	if !strings.Contains(out, "Today    14m") || !strings.Contains(out, "Total    34m") {
		t.Fatalf("totals:\n%s", out)
	}
	if !strings.Contains(out, "cs2") || !strings.Contains(out, "site") {
		t.Fatalf("projects:\n%s", out)
	}
	// cs2 has more total (30m) than site (4m): cs2 listed first
	if strings.Index(out, "cs2") > strings.Index(out, "site") {
		t.Fatalf("order:\n%s", out)
	}
	// attention split present and sums to 100
	if !strings.Contains(out, "hands-on") || !strings.Contains(out, "steering") || !strings.Contains(out, "watching") {
		t.Fatalf("modes:\n%s", out)
	}
	// the three percentages must sum to exactly 100
	var h, s2, w int
	if _, err := fmt.Sscanf(strings.Split(strings.Split(out, "How you worked: ")[1], "\n")[0], "%d%% hands-on · %d%% steering · %d%% watching", &h, &s2, &w); err != nil || h+s2+w != 100 {
		t.Fatalf("pcts %d+%d+%d err=%v", h, s2, w, err)
	}
	// sparkline: the trailing 7 runes of the Total line are block glyphs
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Total") {
			r := []rune(line)
			for _, g := range r[len(r)-7:] {
				if !strings.ContainsRune(string(blocks), g) {
					t.Fatalf("not a sparkline glyph %q in %q", g, line)
				}
			}
		}
	}
}

func TestSparkAndDur(t *testing.T) {
	if spark([]int64{0, 0, 0}) != "   " {
		t.Fatal("empty spark")
	}
	if s := spark([]int64{0, 50, 100}); []rune(s)[0] != ' ' || []rune(s)[2] != '█' {
		t.Fatalf("spark %q", s)
	}
	if dur(90*time.Minute) != "1h 30m" || dur(5*time.Minute) != "5m" {
		t.Fatal("dur")
	}
	if pct(1, 0) != 0 || pct(1, 2) != 50 {
		t.Fatal("pct")
	}
	if truncate("short", 24) != "short" || !strings.HasSuffix(truncate(strings.Repeat("x", 40), 10), "…") {
		t.Fatal("truncate")
	}
}

// Day bucketing must honor the viewer's timezone.
func TestTimezone(t *testing.T) {
	// 02:00 UTC Nov 19 is 21:00 EST Nov 18: these two heartbeats are "today" in UTC but
	// "yesterday" in New York. now is noon UTC Nov 19 (= Nov 19 in both zones).
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	evt := time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
	evs := []event.Event{
		{Time: evt, Kind: event.Heartbeat, Project: "p"},
		{Time: evt.Add(time.Minute), Kind: event.Heartbeat, Project: "p"},
	}
	if out := Render(evs, now, 7, ny); !strings.Contains(out, "Today    0m") {
		t.Fatalf("NY today should be 0:\n%s", out)
	}
	if out := Render(evs, now, 7, utc); !strings.Contains(out, "Today    1m") {
		t.Fatalf("UTC today should be 1m:\n%s", out)
	}
}

func TestRenderShowsUnverified(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	evs := []event.Event{
		{Time: t0, Kind: event.Prompt, Project: "a"},
		{Time: t0.Add(3 * time.Minute), Kind: event.Active},
		{Time: t0.Add(12 * time.Minute), Kind: event.Stop, Project: "a"},
	}
	out := Render(evs, t0.Add(time.Hour), 7, time.UTC)
	if !strings.Contains(out, "Unverified (over the caps, not counted): 7m") {
		t.Fatalf("missing unverified line:\n%s", out)
	}
}
