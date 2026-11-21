# Amp

[Amp](https://ampcode.com) (Sourcegraph) has a TypeScript plugin API, typed by the published
[`@ampcode/plugin`](https://npm.im/@ampcode/plugin) package. vibetime uses every hook it exposes:

| Amp hook | vibetime event |
|---|---|
| `session.start` | `session_start` |
| `agent.start` (user prompt, incl. Ship / Push-to-Branch) | `prompt` |
| `tool.call` | none (completion is `tool.result`) |
| `tool.result` | `edit` for `edit_file`, `create_file`, `apply_patch` (line counts from input); `tool_call` otherwise |
| `agent.end` | `stop` (with the model from the last assistant message) |
| `changes.prompt` | none (a prompt being assembled, not sent; `agent.start` records the real turn) |

It is observe-only: `tool.call` returns `{ action: "allow" }`, and nothing is continued,
modified or injected.

Copy `vibetime.ts` to **`~/.config/amp/plugins/vibetime.ts`** (all projects) or
`<project>/.amp/plugins/vibetime.ts`, then run `plugins: reload` in Amp.

Set `VIBETIME_URL` if your agent isn't on the default `http://127.0.0.1:47391`.

Not verified against a live Amp session yet; the hook names and payload fields come from the SDK types.
