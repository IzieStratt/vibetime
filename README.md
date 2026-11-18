# vibetime

Passive, privacy-friendly time tracking for building with AI. It counts **attended time**: prompting, reviewing, editing, and watching the agent for a few minutes. Agent runtime with nobody at the keyboard doesn't count.

- `vibetime serve`: local agent (loopback only, SQLite, tamper-evident hash chain)
- `vibetime hook <claude-code|codex|gemini-cli>`: harness hook entrypoint (never blocks, spools offline)
- WakaTime-compatible heartbeat endpoint, so existing editor plugins work
- Only per-project totals leave the machine; prompts and code stay local unless you submit

Plugins: `plugins/claude-code`, `plugins/codex`, `plugins/gemini-cli`, `plugins/opencode`.

```sh
go test -race ./...
```

License: AGPL-3.0-only
