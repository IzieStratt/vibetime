# vibetime

Passive, privacy-friendly time tracking for building with AI. vibetime counts **attended time**: prompting, reviewing, editing by hand, and watching the agent work for a few minutes. Time the agent spends running with nobody at the keyboard doesn't count.

## Install (agent)

Download the archive for your OS from Releases (Windows 32/64-bit and ARM, macOS, Linux deb/rpm/tar), or on Nix:

```sh
nix run github:IzieStratt/vibetime           # run once without installing
nix profile install github:IzieStratt/vibetime   # install the agent + server
```

Then:

```sh
vibetime login -server https://YOUR-SERVER -token vt_...   # token from YOUR-SERVER/auth/login
vibetime service install                                   # starts at login, no admin needed
```

Then add the plugin for your harness:

| Harness | How |
|---|---|
| Claude Code | `claude --plugin-dir plugins/claude-code` (or install the plugin) |
| OpenCode ≥ 2 | copy `plugins/opencode/vibetime.ts` to `~/.config/opencode/plugins/` |
| Amp | copy `plugins/amp/vibetime.ts` to `~/.config/amp/plugins/` (or `<project>/.amp/plugins/`), then `plugins: reload` — all Amp hooks (session, prompt, tool, edit, stop) |
| pi | copy `plugins/pi/vibetime.ts` to `~/.pi/agent/extensions/` — prompt-level (prompt, edit, stop) |
| Codex CLI | add `plugins/codex/config.toml` to `~/.codex/config.toml` |
| Gemini CLI | merge `plugins/gemini-cli/settings.json` into `~/.gemini/settings.json` |
| Cursor | copy `plugins/cursor/hooks.json` to `~/.cursor/hooks.json` — prompt-level (prompt, edit, stop, model) |
| GitHub Copilot (agent/CLI) | copy `plugins/copilot/hooks.json` to `<repo>/.github/hooks/` — prompt-level |
| Augment | merge `plugins/augment/settings.json` into `~/.augment/settings.json` — prompt-level |
| Continue (CLI) | add `plugins/continue/hooks.json` to your Continue hooks — prompt-level |
| Any editor (VS Code, Antigravity, Cursor, Windsurf, Zed, Vim/Neovim, JetBrains, Emacs, ...) | Install the editor's WakaTime plugin and set `api_url = http://127.0.0.1:47391/api/v1` in `~/.wakatime.cfg` (any `api_key`). Editor time counts as hands-on. WakaTime's own "ai coding" heartbeats are recorded as agent activity and earn no time |
| Anything else | POST a JSON **array** of events to `http://127.0.0.1:47391/v1/events`, e.g. `[{"time":"...","kind":"prompt","project":"p"}]` (schema: `internal/event`) |

Coming from Hackatime? `vibetime import-hackatime -key YOUR_HACKATIME_KEY` brings your history in, labeled unverified.

Submit a project for a YSWS program:

```sh
vibetime submit -project cs2 -repo github.com/you/cs2 -from 2026-11-01T00:00:00Z
```

Prompts stay on your machine: only their hashes and lengths are uploaded, unless you pass `-include-prompts`. Note that the prompt text itself is stored in plaintext in the local `events.db` (only the hash goes into the chain), so treat that file as you would your shell history — it's private to your account, not encrypted at rest.

See your own time, offline, anytime:

```sh
vibetime status            # today, last 7 days, attention split, per project
vibetime status -days 30
```

Control what gets recorded:

```sh
vibetime exclude ~/work/private-client   # never record anything inside this folder
vibetime exclude -list
vibetime exclude -remove ~/work/private-client
vibetime pause 1h                        # stop recording: 30m, 1h, 2h or "tomorrow"
vibetime resume                          # end a pause early
```

Excluded folders and pauses are checked by the running agent on every event, so no restart is needed. Paused events are dropped (not queued), and presence is not recorded while paused.

Forward WakaTime heartbeats to another endpoint, such as your previous Hackatime API, as well as recording them locally:

```sh
vibetime forward -url https://hackatime.example/api/v1/users/current/heartbeats -key YOUR_KEY
vibetime forward -off
```

Off by default. The copy is sent in the background with a 2-second timeout, so it never slows or breaks the local response. Your key is stored in plaintext in `control.json` in the data directory (mode 0600).

## How hours are made trustworthy

The agent appends every event to a local hash chain and sends the chain head to the server every minute. A submission only counts events covered by heads the server received *at the time*, so history can't be backfilled or made up later. Submissions are also checked server-side and a human reviews anything suspicious.

## Substance caps (ideas from Tally)

Borrowed from [Tally](https://github.com/sectersion/tally), whose "count decisions, not activity" model is a good fit for the steering bucket:

- **Try-again prompts earn little.** A prompt under 20 characters, or one that shares under 15% of its vocabulary with the project's own files, is low effort: repeats within 10 minutes earn at most a minute of watching.
- **Substantive prompts earn think time.** A prompt that clearly refers to the project's code (50% or more relevance) credits up to 5 minutes of composing before it, but only if OS presence covers at least half of those minutes, and never over time already credited.
- **Fail→pass loops earn longer watching.** Test commands run by the agent are recorded as a hash and a pass/fail. The prompt between a failing and a passing run of the same command (within an hour) gets a 20 minute watch cap instead of 5, still bounded by the next event or the agent finishing.
- **Excess is reported, not lost.** Attention beyond a cap, while the agent was still working and you stayed, shows as `Unverified (over the caps, not counted)` in `vibetime status`. It never counts toward hours.
- **README badge.** `https://img.shields.io/endpoint?url=<server>/api/v1/users/<handle>/badge.json` shows your attended hours for the last 30 days (public profiles only).

Relevance is computed locally at hook time and only the number is recorded (`relevance_pm` on the prompt event), never the text. Because it is self-reported, the server treats it as a hint, not proof.

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
