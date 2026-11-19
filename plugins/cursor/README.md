# Cursor

Copy `hooks.json` to `~/.cursor/hooks.json` (all projects) or `<project>/.cursor/hooks.json`.
Cursor sends each hook payload as JSON on stdin and expects a JSON reply; `vibetime hook cursor`
answers `{"continue":true}` so it never blocks a prompt. Docs: https://cursor.com/docs/hooks
