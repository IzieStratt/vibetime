// Package report renders a local, offline summary of attended time for `vibetime status`.
// It reads only the events given to it; nothing leaves the machine.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/attend"
	"github.com/IzieStratt/vibetime/internal/event"
)

// spark renders one day's seconds as a block glyph relative to max.
var blocks = []rune(" ▁▂▃▄▅▆▇█")

func spark(seconds []int64) string {
	var max int64
	for _, s := range seconds {
		if s > max {
			max = s
		}
	}
	var b strings.Builder
	for _, s := range seconds {
		i := 0
		if max > 0 {
			i = int(s * int64(len(blocks)-1) / max)
		}
		b.WriteRune(blocks[i])
	}
	return b.String()
}

func dur(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := d/time.Hour, (d%time.Hour)/time.Minute
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// Render returns a terminal summary for the last `days` days ending at now, in loc.
func Render(events []event.Event, now time.Time, days int, loc *time.Location) string {
	if days < 1 {
		days = 7
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(days - 1))

	perMin := attend.MinutesByMode(events, attend.Default)
	type pstat struct {
		name  string
		total time.Duration
		today time.Duration
		modes attend.MinuteModes
	}
	projects := map[string]*pstat{}
	daily := make([]int64, days) // total seconds per day, oldest first
	var total, todayTotal time.Duration
	var modeTotal attend.MinuteModes

	for name, mins := range perMin {
		ps := &pstat{name: name}
		for unixMin, mm := range mins {
			t := time.Unix(unixMin, 0).In(loc)
			if t.Before(start) {
				continue
			}
			sum := mm[0] + mm[1] + mm[2]
			ps.total += sum
			total += sum
			for i := range ps.modes {
				ps.modes[i] += mm[i]
				modeTotal[i] += mm[i]
			}
			d := daysBetween(start, t, loc)
			if d >= 0 && d < days {
				daily[d] += int64(sum / time.Second)
			}
			if t.Year() == today.Year() && t.YearDay() == today.YearDay() {
				ps.today += sum
				todayTotal += sum
			}
		}
		if ps.total > 0 {
			projects[name] = ps
		}
	}

	var list []*pstat
	for _, p := range projects {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].total != list[j].total {
			return list[i].total > list[j].total
		}
		return list[i].name < list[j].name
	})

	var b strings.Builder
	fmt.Fprintf(&b, "vibetime — last %d days\n\n", days)
	fmt.Fprintf(&b, "  Today    %s\n", dur(todayTotal))
	fmt.Fprintf(&b, "  Total    %s   %s\n\n", dur(total), spark(daily))
	if mt := modeTotal[0] + modeTotal[1] + modeTotal[2]; mt > 0 {
		hands, steer := pct(modeTotal[0], mt), pct(modeTotal[1], mt)
		fmt.Fprintf(&b, "  How you worked: %d%% hands-on · %d%% steering · %d%% watching\n\n",
			hands, steer, 100-hands-steer)
	}
	if len(list) == 0 {
		b.WriteString("  No tracked time yet. Start your AI tool with a vibetime plugin installed.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  %-24s %8s %8s\n", "Project", "today", "total")
	for _, p := range list {
		fmt.Fprintf(&b, "  %-24s %8s %8s\n", truncate(p.name, 24), dur(p.today), dur(p.total))
	}
	return b.String()
}

func pct(part, whole time.Duration) int {
	if whole == 0 {
		return 0
	}
	return int((part*100 + whole/2) / whole)
}

func daysBetween(start, t time.Time, loc *time.Location) int {
	s := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return int(d.Sub(s).Hours() / 24)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
