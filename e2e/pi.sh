#!/usr/bin/env bash
# Real pi (@earendil-works/pi-coding-agent) E2E against a local Ollama model (no account needed).
# Needs: pi on PATH, ollama serving qwen2.5-coder:1.5b, sqlite3.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
S=$(mktemp -d); trap 'pkill -f "serve -addr 127.0.0.1:17894" || true; rm -rf "$S"' EXIT
mkdir -p "$S/bin" "$S/home" "$S/agent/extensions" "$S/proj"
go build -C "$ROOT" -o "$S/bin/" ./cmd/vibetime
git -C "$S/proj" init -q && git -C "$S/proj" remote add origin https://github.com/rowan/pi-demo
cp "$ROOT/plugins/pi/vibetime.ts" "$S/agent/extensions/vibetime.ts"
cat > "$S/agent/models.json" <<JSON
{ "providers": { "ollama": { "baseUrl": "http://localhost:11434/v1", "api": "openai-completions", "apiKey": "ollama",
  "models": [{ "id": "qwen2.5-coder:1.5b", "name": "qwen" }] } } }
JSON
export VIBETIME_HOME="$S/home" VIBETIME_URL=http://127.0.0.1:17894 PI_CODING_AGENT_DIR="$S/agent"
"$S/bin/vibetime" serve -addr 127.0.0.1:17894 2>/dev/null & sleep 1
(cd "$S/proj" && pi --print --model qwen2.5-coder:1.5b "Reply with the single word hello." </dev/null >"$S/out.txt" 2>&1) || { echo "--- pi output"; cat "$S/out.txt"; }
sleep 1
got=$(sqlite3 "$S/home/events.db" "select group_concat(distinct json_extract(body,'\$.kind')||':'||json_extract(body,'\$.harness')||':'||json_extract(body,'\$.project')) from events" 2>/dev/null || true)
echo "events: $got"
[[ "$got" == *"prompt:pi:proj"* && "$got" == *"stop:pi:proj"* ]] || { echo "--- pi output:"; tail -5 "$S/out.txt"; echo FAIL; exit 1; }
echo PASS
