#!/usr/bin/env bash
# Real Codex CLI E2E using a local Ollama model (no account needed).
# Needs: codex, ollama serving qwen2.5-coder:1.5b, sqlite3.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
S=$(mktemp -d); trap 'pkill -f "serve -addr 127.0.0.1:17895" || true; rm -rf "$S"' EXIT
mkdir -p "$S/bin" "$S/home" "$S/codex" "$S/proj"
go build -C "$ROOT" -o "$S/bin/" ./cmd/vibetime
git -C "$S/proj" init -q && git -C "$S/proj" remote add origin git@github.com:rowan/codex-demo.git
printf 'notify = ["%s/bin/vibetime", "hook", "codex"]\n' "$S" > "$S/codex/config.toml"
export VIBETIME_HOME="$S/home" VIBETIME_URL=http://127.0.0.1:17895 CODEX_HOME="$S/codex"
"$S/bin/vibetime" serve -addr 127.0.0.1:17895 2>/dev/null & sleep 1
(cd "$S/proj" && codex exec --oss --local-provider ollama -m qwen2.5-coder:1.5b --skip-git-repo-check "Reply with the word hello." </dev/null >/dev/null 2>&1)
sleep 1
got=$(sqlite3 "$S/home/events.db" "select group_concat(json_extract(body,'\$.kind')||':'||json_extract(body,'\$.repo'), ' ') from events")
echo "events: $got"
[[ "$got" == *"prompt:github.com/rowan/codex-demo"* && "$got" == *"stop:github.com/rowan/codex-demo"* ]] || { echo FAIL; exit 1; }
echo PASS
