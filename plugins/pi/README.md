# pi

[pi](https://github.com/badlogic/pi-mono) (`@earendil-works/pi-coding-agent`) is an extensible
terminal coding-agent harness. vibetime hooks it with a TypeScript **extension**.

Install pi with npm (it has no Homebrew formula): `npm i -g @earendil-works/pi-coding-agent`.

Then copy `vibetime.ts` to **`~/.pi/agent/extensions/vibetime.ts`** (all projects) or
`<project>/.pi/extensions/vibetime.ts`. It subscribes to pi's lifecycle events — `input`
(prompt), `tool_call` (edit/tool), `agent_end` (stop), `session_start` — and forwards
attended-time signals to the local vibetime agent. It never blocks input and never throws.

Set `VIBETIME_URL` if your agent isn't on the default `http://127.0.0.1:47391`.
