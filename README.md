# vibetime

Passive, privacy-friendly time tracking for building with AI. vibetime counts **attended time**: prompting, reviewing, editing by hand, and watching the agent work for a few minutes. Time the agent spends running with nobody at the keyboard doesn't count.

## Install (agent)

Download the archive for your OS from Releases (Windows 32/64-bit and ARM, macOS, Linux deb/rpm/tar), then:

```sh
vibetime login -server https://YOUR-SERVER -token vt_...   # token from YOUR-SERVER/auth/login
vibetime service install                                   # starts at login, no admin needed
```

Then add the plugin for your harness:

| Harness | How |
|---|---|
| Claude Code | `claude --plugin-dir plugins/claude-code` (or install the plugin) |
| OpenCode ≥ 2 | copy `plugins/opencode/vibetime.ts` to `~/.config/opencode/plugins/` |
| Codex CLI | add `plugins/codex/config.toml` to `~/.codex/config.toml` |
| Gemini CLI | merge `plugins/gemini-cli/settings.json` into `~/.gemini/settings.json` |
| Any editor | WakaTime plugins work unchanged: set `api_url = http://127.0.0.1:47391/api/v1` |
| Anything else | POST JSON events to `http://127.0.0.1:47391/v1/events` (schema: `internal/event`) |

Submit a project for a YSWS program:

```sh
vibetime submit -project cs2 -repo github.com/you/cs2 -from 2026-11-01T00:00:00Z
```

Prompts stay on your machine. Only their hashes and lengths are uploaded, unless you pass `-include-prompts`.

## How hours are made trustworthy

The agent appends every event to a local hash chain and sends the chain head to the server every minute. A submission only counts events covered by heads the server received *at the time*, so history can't be backfilled or made up later. Submissions are also checked server-side and a human reviews anything suspicious.

## Server

```sh
vibetime-server -addr :8080 -db vibetime.db -public-url https://vibetime.example \
  -oauth-base https://auth.hackclub.com -webhook https://ysws.example/hook   # OAUTH_CLIENT_ID / OAUTH_CLIENT_SECRET / GITHUB_TOKEN env
vibetime-server -db vibetime.db -add-reviewer fraudsquad   # prints a reviewer token
```

| Path | What |
|---|---|
| `/u/<handle>` | public profile, live over SSE |
| `/review` | reviewer console: queue by risk, timeline, flags, approve / partial / reject |
| `/auth/login` | Hack Club (OIDC) login, which issues an agent token |
| `/api/v1/submissions` | submissions API for programs, with a webhook when status changes |

**Cost:** a single SQLite-backed binary. The load test (`VIBETIME_LOAD=1 go test -run LoadBudget ./internal/server`) sustains ~7,000 syncs/s on a laptop, against the ~2.5/s that Hackatime's 109,759 h/month needs on average. Storage is about 1.6 GB per month at that volume. A ~$10/month VPS fits well under the $500 cap.

## Development

```sh
go test -race ./...                                  # unit + integration
node --test plugins/opencode/vibetime.test.ts        # OpenCode plugin
go build -o bin/ ./cmd/... && (cd e2e && npm ci) && node --test e2e/console.test.mjs   # browser E2E
```

CI runs all of this on Linux, Windows, macOS arm64 and Intel. It also does a real login-service install on Windows and macOS, and checks idle detection on a real X display.

License: AGPL-3.0-only
