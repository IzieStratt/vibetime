# GitHub Copilot (agent mode / Copilot CLI)

Copy `hooks.json` to `<repo>/.github/hooks/vibetime.json`, or `~/.copilot/hooks/vibetime.json`
for all repos. The PascalCase event names make Copilot send Claude-style snake_case payloads,
which `vibetime hook copilot` understands. Docs:
https://code.visualstudio.com/docs/copilot/customization/hooks
