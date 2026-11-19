package server

import (
	"net/http"
	"sort"
	"time"
)

// StreakMinimum is how much attended time makes a day count toward a streak.
const StreakMinimum = 15 * time.Minute

type Day struct {
	Date    string `json:"date"` // YYYY-MM-DD in the requested time zone
	Seconds int64  `json:"seconds"`
}

type Insights struct {
	Handle        string           `json:"handle"`
	Days          []Day            `json:"days"` // last 30 days, oldest first
	CurrentStreak int              `json:"current_streak"`
	LongestStreak int              `json:"longest_streak"`
	Projects      map[string]int64 `json:"projects"`
	// Steering: how much the human drove the work (last 30 days).
	Prompts        int64   `json:"prompts"`
	AILines        int64   `json:"ai_lines"`
	HumanEdits     int64   `json:"human_edits"`
	PromptsPerHour float64 `json:"prompts_per_hour"`
	BestDay        Day     `json:"best_day"`
	WeekSeconds    int64   `json:"week_seconds"`
	PrevWeek       int64   `json:"prev_week_seconds"`
	// Unverified history imported from other trackers; never part of verified totals.
	Imported        map[string]int64 `json:"imported"`
	ImportedSeconds int64            `json:"imported_seconds"`
}

// UserInsights computes motivation stats. Minutes are merged across devices exactly as
// in UserStats (at most 60s per minute), then bucketed by day in loc.
func (s *Server) UserInsights(u user, loc *time.Location) (Insights, error) {
	now := s.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -29)
	rows, err := s.DB.Query(`SELECT project, minute, seconds, prompts, ai_lines, human_edits FROM minutes WHERE user_id = ? AND minute >= ?`,
		u.ID, start.Add(-24*time.Hour).Unix())
	if err != nil {
		return Insights{}, err
	}
	defer rows.Close()
	type pm struct {
		project string
		sec     int64
	}
	perMin := map[int64][]pm{}
	in := Insights{Handle: u.Handle, Projects: map[string]int64{}}
	for rows.Next() {
		var p string
		var m, sec, pr, ai, he int64
		rows.Scan(&p, &m, &sec, &pr, &ai, &he)
		if m < start.Unix() {
			continue
		}
		perMin[m] = append(perMin[m], pm{p, sec})
		in.Prompts += pr
		in.AILines += ai
		in.HumanEdits += he
	}
	if err := rows.Err(); err != nil {
		return in, err
	}
	byDay := map[string]int64{}
	frac := map[string]float64{}
	var total int64
	weekStart, prevStart := today.AddDate(0, 0, -6).Unix(), today.AddDate(0, 0, -13).Unix()
	for m, list := range perMin {
		var sum int64
		for _, x := range list {
			sum += x.sec
		}
		capped := min(sum, 60)
		byDay[time.Unix(m, 0).In(loc).Format("2006-01-02")] += capped
		total += capped
		switch {
		case m >= weekStart:
			in.WeekSeconds += capped
		case m >= prevStart:
			in.PrevWeek += capped
		}
		for _, x := range list {
			if sum > 0 {
				frac[x.project] += float64(x.sec) * float64(capped) / float64(sum)
			}
		}
	}
	for p, f := range frac {
		in.Projects[p] = int64(f + 0.5)
	}
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		day := Day{d.Format("2006-01-02"), byDay[d.Format("2006-01-02")]}
		in.Days = append(in.Days, day)
		if day.Seconds > in.BestDay.Seconds {
			in.BestDay = day
		}
	}
	in.CurrentStreak, in.LongestStreak = streaks(in.Days)
	in.Imported, in.ImportedSeconds = s.importedTotals(u)
	if total > 0 {
		in.PromptsPerHour = float64(in.Prompts) / (float64(total) / 3600)
	}
	return in, nil
}

// streaks: a day qualifies with >= StreakMinimum. The current streak may end today or
// yesterday (today isn't over yet, so it doesn't break a streak).
func streaks(days []Day) (current, longest int) {
	ok := func(d Day) bool { return time.Duration(d.Seconds)*time.Second >= StreakMinimum }
	run := 0
	for _, d := range days {
		if ok(d) {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	i := len(days) - 1
	if i >= 0 && !ok(days[i]) {
		i--
	}
	for ; i >= 0 && ok(days[i]); i-- {
		current++
	}
	return current, longest
}

func (s *Server) insights(w http.ResponseWriter, r *http.Request) {
	u, ok := s.visible(w, r)
	if !ok {
		return
	}
	loc := time.UTC
	if tz := r.URL.Query().Get("tz"); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			fail(w, 400, "unknown time zone")
			return
		}
		loc = l
	}
	in, err := s.UserInsights(u, loc)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, in)
}

// topProjects returns project names sorted by time, for display.
func topProjects(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return m[out[i]] > m[out[j]] || (m[out[i]] == m[out[j]] && out[i] < out[j]) })
	return out
}
