// vibetime extension for pi (@earendil-works/pi-coding-agent, github.com/badlogic/pi-mono).
// Install: copy to ~/.pi/agent/extensions/vibetime.ts (global) or <project>/.pi/extensions/.
// Forwards attended-time signals to the local vibetime agent over its loopback port.
// Never throws: tracking must not disrupt the harness.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const URL = process.env.VIBETIME_URL ?? "http://127.0.0.1:47391";

const lines = (s: unknown) => (typeof s === "string" && s ? s.split("\n").length : 0);

export function projectOf(cwd: string) {
  return cwd.split(/[\\/]/).filter(Boolean).pop() ?? "unknown";
}

// editLines sums lines across pi's edit tool input: {oldText,newText} or a replacements[] array.
export function editLines(input: any) {
  let added = 0, removed = 0;
  const reps = Array.isArray(input?.replacements) ? input.replacements : [input];
  for (const r of reps) { added += lines(r?.newText); removed += lines(r?.oldText); }
  return { added, removed };
}

// toEvent maps one pi lifecycle event to a vibetime event (or null to ignore). Pure/testable.
export function toEvent(channel: string, event: any, cwd: string, now = new Date()) {
  const base = { time: now.toISOString(), project: projectOf(cwd), harness: "pi" as const };
  switch (channel) {
    case "session_start":
      return { ...base, kind: "session_start" };
    case "input":
      if (event?.source === "extension") return null; // our own injected messages, not the human
      return { ...base, kind: "prompt", prompt_text: event?.text };
    case "tool_call": {
      const t = event?.toolName;
      if (t === "write") return { ...base, kind: "edit", lines_added: lines(event?.input?.content) };
      if (t === "edit") {
        const { added, removed } = editLines(event?.input);
        return { ...base, kind: "edit", lines_added: added, lines_removed: removed };
      }
      return { ...base, kind: "tool_call" };
    }
    case "agent_end":
      return { ...base, kind: "stop" };
  }
  return null;
}

export default function (pi: ExtensionAPI) {
  const cwdOf = (ctx: any) => { try { return ctx?.session?.getCwd?.() ?? process.cwd(); } catch { return process.cwd(); } };
  const channels = ["session_start", "input", "tool_call", "agent_end"] as const;
  for (const channel of channels) {
    pi.on(channel, async (event: any, ctx: any) => {
      const e = toEvent(channel, event, cwdOf(ctx));
      if (e) {
        try {
          await fetch(`${URL}/v1/events`, {
            method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify([e]), signal: AbortSignal.timeout(500),
          });
        } catch {}
      }
      if (channel === "input") return { action: "continue" }; // never block input
    });
  }
}
