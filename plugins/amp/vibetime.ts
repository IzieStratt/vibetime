// vibetime plugin for Amp (ampcode.com). Built against the @ampcode/plugin types.
// Install: copy to ~/.config/amp/plugins/vibetime.ts (all projects) or <project>/.amp/plugins/vibetime.ts,
// then run `plugins: reload` in Amp.
// Observe-only: every handler returns a no-op result, so tool calls are never blocked or modified,
// no turn is continued, and no message is injected. Never throws.
const URL = process.env.VIBETIME_URL ?? "http://127.0.0.1:47391";

export const description = "Attended-time tracking for vibetime (local agent on 127.0.0.1:47391).";

const EDIT_TOOLS = new Set(["edit_file", "create_file", "apply_patch"]);

const lines = (s: unknown) => (typeof s === "string" && s !== "" ? s.split("\n").length : 0);

export function projectOf(dir: string) {
  return dir.split(/[\\/]/).filter(Boolean).pop() ?? "unknown";
}

// editCounts reads line counts from a file-edit tool's input.
//  create_file: { content }            edit_file: { old_str, new_str } (or camelCase)
//  apply_patch: { patch } unified diff, counting +/- body lines (not the +++/--- headers)
export function editCounts(tool: string, input: any) {
  const i = input ?? {};
  if (tool === "create_file") return { added: lines(i.content), removed: 0 };
  if (tool === "apply_patch") {
    let added = 0, removed = 0;
    for (const l of String(i.patch ?? "").split("\n")) {
      if (l.startsWith("+") && !l.startsWith("+++")) added++;
      else if (l.startsWith("-") && !l.startsWith("---")) removed++;
    }
    return { added, removed };
  }
  return { added: lines(i.new_str ?? i.newString), removed: lines(i.old_str ?? i.oldString) };
}

// toEvent maps one Amp hook to a vibetime event (or null to ignore). Pure/testable.
// channel: "session.start" | "agent.start" | "tool.call" | "tool.result" | "agent.end" | "changes.prompt"
export function toEvent(channel: string, event: any, dir: string, now = new Date()) {
  const base = { time: now.toISOString(), project: projectOf(dir), harness: "amp" as const };
  switch (channel) {
    case "session.start":
      return { ...base, kind: "session_start" };
    case "agent.start": {
      // Fired for every user prompt (typed, or a Ship / Push-to-Branch click that the client assembled).
      if (typeof event?.message !== "string" || event.message === "") return null;
      return { ...base, kind: "prompt", prompt_text: event.message, cwd: dir };
    }
    case "tool.call":
      return null; // the agent is about to run a tool; tool.result is the completed signal
    case "tool.result": {
      const tool = String(event?.tool ?? "");
      if (EDIT_TOOLS.has(tool)) {
        const { added, removed } = editCounts(tool, event?.input);
        return { ...base, kind: "edit", lines_added: added, lines_removed: removed };
      }
      return { ...base, kind: "tool_call" };
    }
    case "agent.end": {
      // Model from the last assistant message, if Amp reports it.
      const msgs: any[] = Array.isArray(event?.messages) ? event.messages : [];
      const model = [...msgs].reverse().find((m) => m?.role === "assistant" && m?.usage?.model)?.usage?.model;
      return { ...base, kind: "stop", ...(model ? { model } : {}) };
    }
    case "changes.prompt":
      return null; // a prompt being assembled, not yet sent; agent.start records the real turn
  }
  return null;
}

async function send(ev: unknown) {
  try {
    await fetch(`${URL}/v1/events`, {
      method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify([ev]), signal: AbortSignal.timeout(500),
    });
  } catch {}
}

export default function (amp: any) {
  const dirOf = (ctx: any) => {
    try { return String(ctx?.system?.workspaceRoot?.toString?.() ?? amp?.system?.workspaceRoot?.toString?.() ?? process.cwd()); }
    catch { return process.cwd(); }
  };
  const observe = (channel: string) => async (event: any, ctx: any) => {
    try {
      const e = toEvent(channel, event, dirOf(ctx));
      if (e) await send(e);
    } catch {}
  };
  amp.on("session.start", observe("session.start"));
  amp.on("agent.start", observe("agent.start"));
  amp.on("agent.end", async (event: any, ctx: any) => { await observe("agent.end")(event, ctx); });
  amp.on("changes.prompt", observe("changes.prompt"));
  // Request event: must return a result. Allow, unchanged.
  amp.on("tool.call", async () => ({ action: "allow" }));
  amp.on("tool.result", observe("tool.result"));
}
