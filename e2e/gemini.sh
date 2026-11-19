#!/usr/bin/env bash
# Real Gemini CLI E2E against a local fake Gemini API (no account needed). Needs: gemini, node, sqlite3.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
S=$(mktemp -d); trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$S"' EXIT
mkdir -p "$S/bin" "$S/home" "$S/h/.gemini" "$S/proj"
go build -C "$ROOT" -o "$S/bin/" ./cmd/vibetime
git -C "$S/proj" init -q && git -C "$S/proj" remote add origin https://github.com/rowan/gemini-demo
sed "s#\"vibetime hook#\"$S/bin/vibetime hook#g" "$ROOT/plugins/gemini-cli/settings.json" |
  node -e 'let s="";process.stdin.on("data",c=>s+=c).on("end",()=>{const j=JSON.parse(s);j.security={auth:{selectedType:"gemini-api-key"}};process.stdout.write(JSON.stringify(j))})' > "$S/h/.gemini/settings.json"
node "$ROOT/e2e/fake-gemini.mjs" 17897 & 
export VIBETIME_HOME="$S/home" VIBETIME_URL=http://127.0.0.1:17896
"$S/bin/vibetime" serve -addr 127.0.0.1:17896 2>"$S/serve.log" & sleep 1
(cd "$S/proj" && VIBETIME_HOME="$S/home" HOME="$S/h" GEMINI_CLI_TRUST_WORKSPACE=true GEMINI_API_KEY=fake GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:17897 \
  timeout 120 ${GEMINI:-gemini} -p "Reply with the word hello." </dev/null >"$S/out.txt" 2>&1) || { cat "$S/out.txt"; }
sleep 1
got=$(sqlite3 "$S/home/events.db" "select group_concat(json_extract(body,'\$.kind')||':'||json_extract(body,'\$.harness')||':'||json_extract(body,'\$.repo'), ' ') from events" 2>/dev/null || true)
echo "gemini said: $(tail -1 "$S/out.txt")"
echo "events: $got"
[[ "$got" == *"prompt:gemini-cli:github.com/rowan/gemini-demo"* && "$got" == *"stop:gemini-cli"* ]] || { echo "--- gemini output"; cat "$S/out.txt"; echo "--- agent log"; cat "$S/serve.log"; echo "--- home"; ls -la "$S/home" "$S/h/.gemini"; echo FAIL; exit 1; }
echo PASS
