package server

import (
	"html/template"
	"net/http"
)

var profileTmpl = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Handle}} on vibetime</title>
<style>
:root{--bg:#fbfaf7;--fg:#1d1b18;--muted:#6d665c;--line:#e6e1d8;--accent:#c2410c}
@media (prefers-color-scheme:dark){:root{--bg:#151412;--fg:#ece8e1;--muted:#a39b8f;--line:#2d2a26;--accent:#fb923c}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
main{max-width:640px;margin:0 auto;padding:32px 16px}
h1{font-size:28px;margin:0 0 4px}.sub{color:var(--muted);margin:0 0 24px}
.tiles{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:24px}
.tile{border:1px solid var(--line);border-radius:10px;padding:14px}
.tile b{display:block;font-size:26px;font-variant-numeric:tabular-nums}.tile span{color:var(--muted);font-size:13px}
table{width:100%;border-collapse:collapse}td{padding:8px 0;border-bottom:1px solid var(--line)}
td:last-child{text-align:right;font-variant-numeric:tabular-nums}
.live{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--accent);margin-right:6px}
</style></head><body><main>
<h1>{{.Handle}}</h1><p class="sub"><span class="live"></span>attended time building with AI, updating live</p>
<div class="tiles"><div class="tile"><b id="today">–</b><span>today</span></div><div class="tile"><b id="week">–</b><span>last 7 days</span></div></div>
<table id="projects"></table>
</main>
<script>
const fmt = s => { const h = Math.floor(s/3600), m = Math.floor(s%3600/60); return h ? h+"h "+m+"m" : m+"m"; };
const es = new EventSource({{.LiveURL}});
es.onmessage = e => {
  const s = JSON.parse(e.data);
  document.getElementById("today").textContent = fmt(s.today_seconds);
  document.getElementById("week").textContent = fmt(s.week_seconds);
  const t = document.getElementById("projects"); t.replaceChildren();
  Object.entries(s.projects).sort((a,b)=>b[1]-a[1]).forEach(([p,sec]) => {
    const r = t.insertRow(); r.insertCell().textContent = p; r.insertCell().textContent = fmt(sec);
  });
};
</script></body></html>`))

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	u, ok := s.lookup(r.PathValue("handle"))
	if !ok || !u.Public {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
	profileTmpl.Execute(w, map[string]string{"Handle": u.Handle, "LiveURL": "/api/v1/users/" + u.Handle + "/live"})
}
