// OpenCode (>= 2) plugin for vibetime.
// Install: copy to ~/.config/opencode/plugins/vibetime.ts (or <project>/.opencode/plugins/).
// Listens to OpenCode's event bus and forwards attended-time signals to the local agent.
// Options: { "url": "http://127.0.0.1:47391" } (OpenCode runs plugins in a shared background
// server, so a shell VIBETIME_URL may not reach it). Never throws: tracking must not break the harness.
const DEFAULT_URL = process.env.VIBETIME_URL ?? "http://127.0.0.1:47391";

export type VTEvent = {
  time: string;
  kind: string;
  project: string;
  harness: "opencode";
  lines_added?: number;
  lines_removed?: number;
  prompt_text?: string;
};

const lines = (s: unknown) => (typeof s === "string" && s !== "" ? s.split("\n").length : 0);

export function projectOf(dir: string) {
  return dir.split(/[\\/]/).filter(Boolean).pop() ?? "unknown";
}

// toEvent maps one OpenCode bus event to a vibetime event (or null to ignore).
export function toEvent(e: any, project: string, now = new Date()): VTEvent | null {
  const base = { time: now.toISOString(), project, harness: "opencode" as const };
  const d = e?.data ?? {};
  switch (e?.type) {
    case "session.created":
      return { ...base, kind: "session_start" };
    case "session.inbox.enqueued":
      if (d.item?.type !== "user") return null;
      return { ...base, kind: "prompt", prompt_text: d.item?.payload?.text };
    case "session.tool.called": {
      const i = d.input ?? {};
      const added = lines(i.content) + lines(i.newString) + lines(i.new_string);
      const removed = lines(i.oldString) + lines(i.old_string);
      if (added || removed || "patch" in i) return { ...base, kind: "edit", lines_added: added, lines_removed: removed };
      return { ...base, kind: "tool_call" };
    }
    case "session.execution.succeeded":
    case "session.execution.failed":
      return { ...base, kind: "stop" };
  }
  return null;
}

async function send(url: string, ev: VTEvent) {
  try {
    await fetch(`${url}/v1/events`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify([ev]),
      signal: AbortSignal.timeout(500),
    });
  } catch {}
}

export default {
  id: "vibetime",
  setup(ctx: any) {
    const url = String(ctx.options?.url ?? DEFAULT_URL);
    const project = projectOf(ctx.location?.directory ?? process.cwd());
    const stream = ctx.event.subscribe();
    (async () => {
      try {
        for await (const e of stream) {
          const ev = toEvent(e, project);
          if (ev) void send(url, ev);
        }
      } catch {}
    })();
  },
};
