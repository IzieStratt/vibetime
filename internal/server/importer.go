package server

import (
	"net/http"
	"regexp"
	"time"
)

// ImportDay is one day of history from another tracker (e.g. Hackatime). Imported time is
// shown on profiles as unverified and never enters submissions or verified totals (C4).
type ImportDay struct {
	Date    string `json:"date"` // YYYY-MM-DD
	Project string `json:"project"`
	Seconds int64  `json:"seconds"`
}

type ImportRequest struct {
	Source string      `json:"source"` // "hackatime"
	Days   []ImportDay `json:"days"`
}

var sourceRe = regexp.MustCompile(`^[a-z0-9_-]{2,32}$`)

const importSchema = `CREATE TABLE IF NOT EXISTS imported (user_id INTEGER NOT NULL, source TEXT NOT NULL, date TEXT NOT NULL,
  project TEXT NOT NULL, seconds INTEGER NOT NULL, PRIMARY KEY (user_id, source, date, project))`

func (s *Server) importHistory(w http.ResponseWriter, r *http.Request, u user) {
	var in ImportRequest
	if !decode(w, r, &in, 8<<20) {
		return
	}
	if !sourceRe.MatchString(in.Source) {
		fail(w, 400, "bad source")
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	ok, bad := 0, 0
	for _, d := range in.Days {
		if _, err := time.Parse("2006-01-02", d.Date); err != nil || d.Project == "" || len(d.Project) > 200 || d.Seconds <= 0 {
			bad++
			continue
		}
		tx.Exec(`INSERT INTO imported(user_id, source, date, project, seconds) VALUES(?,?,?,?,?)
			ON CONFLICT(user_id, source, date, project) DO UPDATE SET seconds = excluded.seconds`,
			u.ID, in.Source, d.Date, d.Project, min(d.Seconds, 86400))
		ok++
	}
	if err := tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.hub.publish(u.ID)
	writeJSON(w, 200, map[string]int{"imported": ok, "rejected": bad})
}

// importedTotals returns unverified imported seconds per project.
func (s *Server) importedTotals(u user) (map[string]int64, int64) {
	out := map[string]int64{}
	var total int64
	rows, err := s.DB.Query(`SELECT project, SUM(seconds) FROM imported WHERE user_id = ? GROUP BY project`, u.ID)
	if err != nil {
		return out, 0
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var sec int64
		rows.Scan(&p, &sec)
		out[p] = sec
		total += sec
	}
	return out, total
}
