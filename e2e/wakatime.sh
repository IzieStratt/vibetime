#!/usr/bin/env bash
# Real wakatime-cli E2E: the path every WakaTime editor plugin (VS Code, Antigravity, Cursor,
# Zed, Vim/Neovim, JetBrains, ...) uses. Needs: wakatime-cli, sqlite3.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
S=$(mktemp -d); trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$S"' EXIT
mkdir -p "$S/proj" && git -C "$S/proj" init -q && echo 'package main' > "$S/proj/main.go"
go build -C "$ROOT" -o "$S/" ./cmd/vibetime
printf '[settings]\napi_url = http://127.0.0.1:17898/api/v1\napi_key = waka_00000000-0000-4000-8000-000000000000\n' > "$S/wakatime.cfg"
"$S/vibetime" serve -addr 127.0.0.1:17898 -db "$S/e.db" 2>/dev/null & sleep 1
now=$(date +%s)
# newer wakatime-cli scans AI tools' transcripts on every heartbeat; skip that in tests
noai=""; wakatime-cli --help 2>&1 | grep -q -- --sync-ai-disabled && noai=--sync-ai-disabled
i=0
for p in "neovim/0.11 vim-wakatime/11.0" "vscode/1.106 vscode-wakatime/25.0" "zed/0.210 zed-wakatime/0.1"; do
  WAKATIME_HOME="$S" wakatime-cli --config "$S/wakatime.cfg" $noai --heartbeat-rate-limit-seconds 0 \
    --entity "$S/proj/main.go" --plugin "$p" --time $((now - 120 + i * 60)) --write --timeout 10 </dev/null
  i=$((i + 1))
done
got=$(sqlite3 "$S/e.db" "select count(*)||':'||group_concat(distinct json_extract(body,'\$.project'))||':'||group_concat(distinct json_extract(body,'\$.language')) from events")
sum=$(curl -s 127.0.0.1:17898/v1/summary)
echo "events: $got  summary: $sum"
[[ "$got" == "3:proj:Go" && "$sum" == *'"total_seconds":120'* ]] || { echo FAIL; exit 1; }
echo PASS
