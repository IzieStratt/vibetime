// OpenCode plugin: copy to ~/.config/opencode/plugin/vibetime.ts
// Translates OpenCode events into Claude-style hook payloads for `vibetime hook claude-code`.
const URL = process.env.VIBETIME_URL ?? "http://127.0.0.1:7878";

async function send(kind: string, cwd: string, extra: Record<string, unknown> = {}) {
  try {
    await fetch(`${URL}/v1/events`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify([{ time: new Date().toISOString(), kind, project: cwd.split(/[\\/]/).filter(Boolean).pop(), harness: "opencode", ...extra }]),
      signal: AbortSignal.timeout(500),
    });
  } catch {} // never break the harness
}

export const Vibetime = async ({ directory, worktree }: { directory: string; worktree?: string }) => {
  const root = worktree || directory;
  return {
    "chat.message": async () => send("prompt", root),
    "tool.execute.after": async (input: { tool: string }) =>
      send(["edit", "write", "patch"].includes(input.tool) ? "edit" : "tool_call", root),
    event: async ({ event }: { event: { type: string } }) => {
      if (event.type === "session.idle") await send("stop", root);
    },
  };
};
