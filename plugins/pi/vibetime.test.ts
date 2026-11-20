import { test } from "node:test";
import assert from "node:assert/strict";
import { toEvent, projectOf, editLines } from "./vibetime.ts";

const now = new Date("2026-11-20T09:00:00Z");
const ev = (channel: string, event: unknown = {}) => toEvent(channel, event, "/home/kid/cs2", now);

test("project from cwd", () => {
  assert.equal(projectOf("/a/b/cs2"), "cs2");
  assert.equal(projectOf("C:\\x\\game\\"), "game");
  assert.equal(projectOf(""), "unknown");
});

test("session start and agent end", () => {
  assert.equal(ev("session_start")?.kind, "session_start");
  assert.equal(ev("agent_end")?.kind, "stop");
});

test("input: human prompt vs extension-injected", () => {
  const p = ev("input", { source: "interactive", text: "fix recoil" });
  assert.deepEqual(p, { time: now.toISOString(), project: "cs2", harness: "pi", kind: "prompt", prompt_text: "fix recoil" });
  assert.equal(ev("input", { source: "rpc", text: "x" })?.kind, "prompt");
  assert.equal(ev("input", { source: "extension", text: "injected" }), null);
});

test("tool calls: write/edit vs other", () => {
  assert.deepEqual(
    [ev("tool_call", { toolName: "write", input: { path: "a", content: "x\ny" } })?.kind,
     ev("tool_call", { toolName: "write", input: { path: "a", content: "x\ny" } })?.lines_added],
    ["edit", 2],
  );
  const e = ev("tool_call", { toolName: "edit", input: { path: "a", oldText: "a", newText: "b\nc" } });
  assert.equal(e?.lines_added, 2);
  assert.equal(e?.lines_removed, 1);
  assert.equal(ev("tool_call", { toolName: "bash", input: { command: "ls" } })?.kind, "tool_call");
  assert.equal(ev("tool_call", { toolName: "read", input: { path: "a" } })?.kind, "tool_call");
});

test("edit replacements[] array", () => {
  const { added, removed } = editLines({ replacements: [{ oldText: "a", newText: "b\nc" }, { oldText: "d\ne", newText: "f" }] });
  assert.equal(added, 3);
  assert.equal(removed, 3);
});

test("ignored channels", () => {
  assert.equal(ev("turn_start"), null);
  assert.equal(ev("message_update"), null);
});
