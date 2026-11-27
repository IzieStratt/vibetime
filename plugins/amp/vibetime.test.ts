import { test } from "node:test";
import assert from "node:assert/strict";
import plugin, { toEvent, projectOf, editCounts } from "./vibetime.ts";

const now = new Date("2026-11-20T09:00:00Z");
const ev = (channel: string, event: unknown = {}) => toEvent(channel, event, "/home/kid/cs2", now);

test("project from dir", () => {
  assert.equal(projectOf("/a/b/cs2"), "cs2");
  assert.equal(projectOf("C:\\x\\game\\"), "game");
  assert.equal(projectOf(""), "unknown");
});

test("session start and agent end", () => {
  assert.equal(ev("session.start")?.kind, "session_start");
  assert.equal(ev("agent.end", { messages: [] })?.kind, "stop");
  assert.equal(ev("agent.end", { messages: [] })?.harness, "amp");
});

test("agent.end reports model from last assistant usage", () => {
  const messages = [
    { role: "user", content: [] },
    { role: "assistant", usage: { model: "claude-sonnet-5-5" } },
  ];
  assert.equal(ev("agent.end", { messages })?.model, "claude-sonnet-5-5");
  assert.equal(ev("agent.end", { messages: [] })?.model, undefined);
});

test("agent.start carries the prompt", () => {
  assert.deepEqual(ev("agent.start", { message: "fix recoil" }), {
    time: now.toISOString(), project: "cs2", harness: "amp", kind: "prompt", prompt_text: "fix recoil", cwd: "/home/kid/cs2",
  });
  assert.equal(ev("agent.start", { message: "" }), null);
});

test("tool.result: edit tools counted, other tools are tool_call", () => {
  const out = [
    ev("tool.result", { tool: "create_file", input: { content: "x\ny" } })?.lines_added,
    ev("tool.result", { tool: "edit_file", input: { old_str: "a", new_str: "b\nc" } })?.lines_added,
    ev("tool.result", { tool: "edit_file", input: { old_str: "a", new_str: "b\nc" } })?.lines_removed,
    ev("tool.result", { tool: "apply_patch", input: { patch: "--- a\n+++ b\n@@\n-x\n+y\n+z" } })?.lines_added,
    ev("tool.result", { tool: "Bash", input: { cmd: "ls" } })?.kind,
  ];
  assert.deepEqual(out, [2, 2, 1, 2, "tool_call"]);
});

test("tool.call and changes.prompt produce no event", () => {
  assert.equal(ev("tool.call", { tool: "edit_file" }), null);
  assert.equal(ev("changes.prompt", { workflow: "ship" }), null);
});

test("editCounts tolerates missing input", () => {
  assert.deepEqual(editCounts("edit_file", undefined), { added: 0, removed: 0 });
});

test("default export subscribes to every hook and never modifies tool calls", async () => {
  const handlers: Record<string, (...a: any[]) => any> = {};
  const sent: any[] = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = (async (_u: any, init: any) => { sent.push(JSON.parse(init.body)[0]); return new Response(""); }) as any;
  try {
    plugin({ on: (name: string, h: any) => { handlers[name] = h; } } as any);
    assert.deepEqual(Object.keys(handlers).sort(),
      ["agent.end", "agent.start", "changes.prompt", "session.start", "tool.call", "tool.result"]);
    assert.deepEqual(await handlers["tool.call"]({ tool: "edit_file" }, {}), { action: "allow" });
    assert.equal(await handlers["agent.end"]({ messages: [] }, {}), undefined);
    await handlers["agent.start"]({ message: "hi" }, {});
    assert.equal(sent.at(-1).kind, "prompt");
  } finally {
    globalThis.fetch = realFetch;
  }
});
