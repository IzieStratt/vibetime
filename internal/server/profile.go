package server

import (
	"html/template"
	"net/http"
)

var profileTmpl = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Handle}} on vibetime</title>
<style>
:root{--bg:#fbfaf7;--fg:#1d1b18;--muted:#6d665c;--line:#e6e1d8;--bar:#c2410c;--bar-dim:#f1dccf;--card:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#151412;--fg:#ece8e1;--muted:#a39b8f;--line:#2d2a26;--bar:#fb923c;--bar-dim:#3a2a20;--card:#1d1c19}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
main{max-width:720px;margin:0 auto;padding:32px 16px}
h1{font-size:28px;margin:0 0 4px}h2{font-size:15px;color:var(--muted);font-weight:600;margin:28px 0 8px}
.sub{color:var(--muted);margin:0 0 20px}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px}
.tile{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 14px}
.tile b{display:block;font-size:24px;font-variant-numeric:tabular-nums;line-height:1.2}.tile span{color:var(--muted);font-size:13px}
.chart{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px}
svg{display:block;width:100%;height:auto}
.axis{display:flex;justify-content:space-between;color:var(--muted);font-size:12px;margin-top:4px}
table{width:100%;border-collapse:collapse}td{padding:8px 0;border-bottom:1px solid var(--line)}
td:last-child{text-align:right;font-variant-numeric:tabular-nums;color:var(--muted)}
.live{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--bar);margin-right:6px}
#tip{color:var(--muted);font-size:13px;min-height:1.5em;margin-top:6px}
</style></head><body><main>
<h1>{{.Handle}}</h1><p class="sub"><span class="live"></span>Attended time building with AI. Updates live.</p>
<div class="tiles">
  <div class="tile"><b id="today">–</b><span>today</span></div>
  <div class="tile"><b id="week">–</b><span id="weekd">last 7 days</span></div>
  <div class="tile"><b id="streak">–</b><span id="streakd">day streak</span></div>
  <div class="tile"><b id="best">–</b><span id="bestd">best day</span></div>
</div>
<h2>Last 30 days</h2>
<div class="chart"><svg id="bars" viewBox="0 0 300 80" role="img" aria-label="Attended time per day, last 30 days"></svg>
<div class="axis"><span id="first"></span><span>today</span></div><div id="tip">Days with 15+ minutes keep your streak going.</div></div>
<h2>How you steered</h2>
<div class="tiles">
  <div class="tile"><b id="prompts">–</b><span>prompts</span></div>
  <div class="tile"><b id="pph">–</b><span>prompts per hour</span></div>
  <div class="tile"><b id="human">–</b><span>edits by hand</span></div>
  <div class="tile"><b id="ai">–</b><span>lines changed by AI</span></div>
</div>
<h2>Projects</h2>
<table id="projects"></table>
<div id="imp" hidden><h2>Imported history (unverified)</h2>
<p class="sub">From Hackatime before vibetime. Shown for context; it never counts toward submissions.</p>
<table id="imported"></table></div>
</main>
<script>
const handle = {{.Handle}}, tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
const fmt = s => { const h = Math.floor(s/3600), m = Math.floor(s%3600/60); return h ? h+"h "+m+"m" : m+"m"; };
const set = (id, v) => document.getElementById(id).textContent = v;
const NS = "http://www.w3.org/2000/svg";
function bars(days) {
  const svg = document.getElementById("bars"); svg.replaceChildren();
  const max = Math.max(3600, ...days.map(d => d.seconds)), w = 300 / days.length;
  days.forEach(d => {
    const h = Math.max(d.seconds ? 2 : 0, (d.seconds / max) * 76), r = document.createElementNS(NS, "rect");
    r.setAttribute("x", w * days.indexOf(d) + 1); r.setAttribute("width", w - 2);
    r.setAttribute("y", 80 - h); r.setAttribute("height", h); r.setAttribute("rx", 1.5);
    r.setAttribute("fill", d.seconds >= 900 ? "var(--bar)" : "var(--bar-dim)");
    const t = document.createElementNS(NS, "title"); t.textContent = d.date + ": " + fmt(d.seconds); r.append(t);
    r.addEventListener("mouseenter", () => set("tip", d.date + ": " + fmt(d.seconds)));
    svg.append(r);
  });
  set("first", days[0] ? days[0].date.slice(5) : "");
}
async function refresh() {
  const r = await fetch("/api/v1/users/" + encodeURIComponent(handle) + "/insights?tz=" + encodeURIComponent(tz));
  if (!r.ok) return;
  const s = await r.json(), today = s.days[s.days.length - 1];
  set("today", fmt(today.seconds)); set("week", fmt(s.week_seconds));
  const diff = s.week_seconds - s.prev_week_seconds;
  set("weekd", s.prev_week_seconds ? (diff >= 0 ? "▲ " : "▼ ") + fmt(Math.abs(diff)) + " vs week before" : "last 7 days");
  set("streak", s.current_streak); set("streakd", "day streak · best " + s.longest_streak);
  set("best", fmt(s.best_day.seconds)); set("bestd", s.best_day.seconds ? "best day · " + s.best_day.date.slice(5) : "best day");
  set("prompts", s.prompts.toLocaleString()); set("pph", s.prompts_per_hour.toFixed(1));
  set("human", s.human_edits.toLocaleString()); set("ai", s.ai_lines.toLocaleString());
  bars(s.days);
  const t = document.getElementById("projects"); t.replaceChildren();
  Object.entries(s.projects).sort((a, b) => b[1] - a[1]).forEach(([p, sec]) => {
    const row = t.insertRow(); row.insertCell().textContent = p; row.insertCell().textContent = fmt(sec);
  });
  const imp = Object.entries(s.imported || {}); document.getElementById("imp").hidden = !imp.length;
  const it = document.getElementById("imported"); it.replaceChildren();
  imp.sort((a, b) => b[1] - a[1]).forEach(([p, sec]) => { const row = it.insertRow(); row.insertCell().textContent = p; row.insertCell().textContent = fmt(sec); });
}
refresh();
new EventSource({{.LiveURL}}).onmessage = refresh;
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
