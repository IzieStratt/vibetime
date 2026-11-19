package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

func TestStreaks(t *testing.T) {
	d := func(mins ...int) []Day {
		var out []Day
		for _, m := range mins {
			out = append(out, Day{Seconds: int64(m * 60)})
		}
		return out
	}
	cases := []struct {
		days         []Day
		cur, longest int
	}{
		{nil, 0, 0},
		{d(20, 20, 0, 30, 30, 30), 3, 3},
		{d(20, 20, 20, 0), 3, 3},    // today not done yet: streak kept
		{d(20, 20, 20, 0, 0), 0, 3}, // missed yesterday: broken
		{d(20, 14, 20), 1, 1},       // 14 min is under the bar
		{d(60, 60, 60, 60, 0, 20), 1, 4},
	}
	for i, c := range cases {
		if cur, l := streaks(c.days); cur != c.cur || l != c.longest {
			t.Errorf("%d: got %d/%d want %d/%d", i, cur, l, c.cur, c.longest)
		}
	}
}

func TestInsightsEndToEnd(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	a := e.agent(tok)
	a.add(event.Event{Time: t0, Kind: event.Prompt, Project: "cs2"})
	b := e.agent(tok)
	b.dev = "device-0002"
	// three consecutive days of 20 minutes, synced live each day; day 2 also on a second device
	for day := 0; day < 3; day++ {
		base := t0.AddDate(0, 0, day)
		now := base.Add(20 * time.Minute)
		var mins []SyncMinute
		for i := 1; i <= 14; i++ { // within the sync window
			mins = append(mins, SyncMinute{Project: "cs2", Minute: now.Add(-time.Duration(i) * time.Minute).Unix(), Seconds: 60, Prompts: 1, AILines: 10})
		}
		mins[0].HumanEdits = 2
		a.sync(now, mins...)
		a.sync(now.Add(6*time.Minute), SyncMinute{Project: "cs2", Minute: now.Add(5 * time.Minute).Unix(), Seconds: 60},
			SyncMinute{Project: "web", Minute: now.Unix(), Seconds: 30})
		if day == 1 {
			b.sync(now, SyncMinute{Project: "web", Minute: now.Add(-2 * time.Minute).Unix(), Seconds: 60}) // overlaps: must not add time
		}
	}
	e.at(t0.AddDate(0, 0, 2).Add(time.Hour))
	c, body := e.call("GET", "/api/v1/users/rowan/insights?tz=America/New_York", "", nil)
	var in Insights
	json.Unmarshal(body, &in)
	if c != 200 || len(in.Days) != 30 {
		t.Fatalf("%d %s", c, body)
	}
	last := in.Days[len(in.Days)-1]
	if last.Date != "2026-11-20" || last.Seconds != 15*60+30 {
		t.Fatalf("today %+v", last)
	}
	if in.CurrentStreak != 3 || in.LongestStreak != 3 {
		t.Fatalf("streaks %d %d", in.CurrentStreak, in.LongestStreak)
	}
	if in.Prompts != 42 || in.AILines != 420 || in.HumanEdits != 6 {
		t.Fatalf("steering %+v", in)
	}
	if in.WeekSeconds != 3*(15*60+30) || in.PromptsPerHour < 50 || in.BestDay.Seconds != 15*60+30 {
		t.Fatalf("%+v", in)
	}
	if got := topProjects(in.Projects); got[0] != "cs2" || len(got) != 2 {
		t.Fatal(got)
	}
	if c, _ := e.call("GET", "/api/v1/users/rowan/insights?tz=Mars/Base", "", nil); c != 400 {
		t.Fatal("bad tz")
	}
	if c, _ := e.call("GET", "/api/v1/users/nobody/insights", "", nil); c != 404 {
		t.Fatal("missing user")
	}
}

func TestImportEndpointAndInsights(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	c, b := e.call("POST", "/api/v1/import", tok, ImportRequest{Source: "hackatime", Days: []ImportDay{
		{"2026-10-01", "cs2", 7200}, {"2026-10-02", "cs2", 999999}, {"bad-date", "x", 5}, {"2026-10-03", "", 5}, {"2026-10-03", "y", 0}}})
	if c != 200 || !strings.Contains(string(b), `"imported":2`) || !strings.Contains(string(b), `"rejected":3`) {
		t.Fatalf("%d %s", c, b)
	}
	for _, body := range []any{"x", ImportRequest{Source: "Bad Source!"}} {
		if c, _ := e.call("POST", "/api/v1/import", tok, body); c != 400 {
			t.Errorf("accepted %v", body)
		}
	}
	_, b = e.call("GET", "/api/v1/users/rowan/insights", "", nil)
	var in Insights
	json.Unmarshal(b, &in)
	if in.ImportedSeconds != 7200+86400 || in.Imported["cs2"] != 7200+86400 || in.WeekSeconds != 0 || len(in.Projects) != 0 {
		t.Fatalf("%+v", in)
	}
}
