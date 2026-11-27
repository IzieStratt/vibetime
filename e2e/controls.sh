#!/usr/bin/env bash
# E2E for the local controls, run against the real binary on a throwaway port and data dir:
# exclude, pause/resume, test-run detection and prompt retention. Needs: go, sqlite3.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
S=$(mktemp -d); trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$S"' EXIT
mkdir -p "$S/bin" "$S/home" "$S/open" "$S/secret"
go build -C "$ROOT" -o "$S/bin/" ./cmd/vibetime
VT="$S/bin/vibetime"
export VIBETIME_HOME="$S/home" VIBETIME_URL=http://127.0.0.1:17898
for d in open secret; do git -C "$S/$d" init -q; done
"$VT" serve -addr 127.0.0.1:17898 2>"$S/serve.log" & sleep 1
count() { sqlite3 -readonly "$S/home/events.db" "select count(*) from events where json_extract(body,'\$.kind') = '$1'"; }
hook() { printf '{"hook_event_name":"%s","cwd":"%s","prompt":"%s","tool_name":"%s","tool_input":{"command":"%s"}}' "$1" "$2" "${3:-}" "${4:-}" "${5:-}" | "$VT" hook claude-code; }
fail() { echo "FAIL: $1"; cat "$S/serve.log"; exit 1; }

hook UserPromptSubmit "$S/open" "fix the retry loop in the syncer please"; sleep 0.3
[ "$(count prompt)" = 1 ] || fail "prompt in a normal folder was not recorded"

"$VT" exclude "$S/secret" >/dev/null
hook UserPromptSubmit "$S/secret" "this must never be recorded anywhere"; sleep 0.3
[ "$(count prompt)" = 1 ] || fail "excluded folder was recorded"

"$VT" pause 1h >/dev/null
hook UserPromptSubmit "$S/open" "paused so this is dropped as well"; sleep 0.3
[ "$(count prompt)" = 1 ] || fail "recorded while paused"
"$VT" resume >/dev/null
hook UserPromptSubmit "$S/open" "resumed so this one is recorded again"; sleep 0.3
[ "$(count prompt)" = 2 ] || fail "not recording after resume"

hook PostToolUseFailure "$S/open" "" Bash "go test ./..."; sleep 0.3
hook PostToolUse "$S/open" "" Bash "go test ./..."; sleep 0.3
[ "$(count test_run)" = 2 ] || fail "test runs not detected"
! sqlite3 -readonly "$S/home/events.db" "select body from events" | grep -q "go test" || fail "test command text was stored"
! sqlite3 -readonly "$S/home/events.db" "select body from events" | grep -q '"cwd"' || fail "cwd was stored"
echo PASS
