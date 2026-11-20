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
| pi | copy `plugins/pi/vibetime.ts` to `~/.pi/agent/extensions/` — prompt-level (prompt, edit, stop) |
| Codex CLI | add `plugins/codex/config.toml` to `~/.codex/config.toml` |
| Gemini CLI | merge `plugins/gemini-cli/settings.json` into `~/.gemini/settings.json` |
| Cursor | copy `plugins/cursor/hooks.json` to `~/.cursor/hooks.json` — prompt-level (prompt, edit, stop, model) |
| GitHub Copilot (agent/CLI) | copy `plugins/copilot/hooks.json` to `<repo>/.github/hooks/` — prompt-level |
| Augment | merge `plugins/augment/settings.json` into `~/.augment/settings.json` — prompt-level |
| Continue (CLI) | add `plugins/continue/hooks.json` to your Continue hooks — prompt-level |
| Any editor (VS Code, Antigravity, Cursor, Windsurf, Zed, Vim/Neovim, JetBrains, Emacs, ...) | Install the editor's WakaTime plugin and set `api_url = http://127.0.0.1:47391/api/v1` in `~/.wakatime.cfg` (any `api_key`). Editor time counts as hands-on. WakaTime's own "ai coding" heartbeats are recorded as agent activity and earn no time |
| Anything else | POST JSON events to `http://127.0.0.1:47391/v1/events` (schema: `internal/event`) |

Coming from Hackatime? `vibetime import-hackatime -key YOUR_HACKATIME_KEY` brings your history in, labeled unverified.

Submit a project for a YSWS program:

```sh
vibetime submit -project cs2 -repo github.com/you/cs2 -from 2026-11-01T00:00:00Z
```

Prompts stay on your machine. Only their hashes and lengths are uploaded, unless you pass `-include-prompts`.

See your own time, offline, anytime:

```sh
vibetime status            # today, last 7 days, attention split, per project
vibetime status -days 30
```

## How hours are made trustworthy

The agent appends every event to a local hash chain and sends the chain head to the server every minute. A submission only counts events covered by heads the server received *at the time*, so history can't be backfilled or made up later. Fraud detectors run on top of that, and their flags send a submission to a human. Every attack we know of, with the code and test for it, is in [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md).

## Server

**Hack Club login:** turn on developer mode in your Hack Club Auth account, then create an app at <https://auth.hackclub.com/developer/apps>:
- redirect URI `https://<your server>/auth/callback`
- scopes `openid profile email slack_id verification_status` (all allowed for community apps)

Its client ID and secret go in the env vars below. Email, Slack ID and YSWS eligibility are stored privately. They never appear on profiles, only in submissions for reviewers and programs.


```sh
OAUTH_CLIENT_ID=... OAUTH_CLIENT_SECRET=... GITHUB_TOKEN=... \
vibetime-server -addr :8080 -db vibetime.db -public-url https://vibetime.example -webhook https://ysws.example/hook
vibetime-server -db vibetime.db -add-reviewer fraudsquad   # prints a reviewer token
```

| Path | What |
|---|---|
| `/u/<handle>` | public profile, live over SSE |
| `/review` | reviewer console: queue by risk, timeline, flags, approve / partial / reject |
| `/auth/login` | Hack Club (OIDC) login, which issues an agent token |
| `/api/v1/submissions` | submissions API for programs, with a webhook when status changes. Each submission has `unified.airtable` with the Unified YSWS DB field names (`Code URL`, `Email`, `slack_id`, `Optional - Override Hours Spent`, `Optional - Override Hours Spent Justification`) |

**Cost:** a single SQLite-backed binary. The load test (`VIBETIME_LOAD=1 go test -run LoadBudget ./internal/server`) sustains ~7,000 syncs/s on a laptop, against the ~2.5/s that Hackatime's 109,759 h/month needs on average. Storage is about 1.6 GB per month at that volume. A ~$10/month VPS fits well under the $500 cap.

## Development

```sh
go test -race ./...                                  # unit + integration
node --test plugins/opencode/vibetime.test.ts        # OpenCode plugin
go build -o bin/ ./cmd/... && (cd e2e && npm ci) && node --test e2e/console.test.mjs   # browser E2E
```

CI runs all of this on Linux, Windows, macOS arm64 and Intel. It also does a real login-service install on Windows and macOS, and checks idle detection on a real X display.

License: AGPL-3.0-only
