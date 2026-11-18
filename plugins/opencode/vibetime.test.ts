import { test } from "node:test";
import assert from "node:assert/strict";
import { toEvent, projectOf } from "./vibetime.ts";

const now = new Date("2026-11-18T09:00:00Z");
const ev = (type: string, data: unknown = {}) => toEvent({ type, data }, "game", now);

test("project name from directory", () => {
  assert.equal(projectOf("/a/b/game"), "game");
  assert.equal(projectOf("C:\\x\\game\\"), "game");
  assert.equal(projectOf(""), "unknown");
});

test("session and stop", () => {
  assert.equal(ev("session.created")?.kind, "session_start");
  assert.equal(ev("session.execution.succeeded")?.kind, "stop");
  assert.equal(ev("session.execution.failed")?.kind, "stop");
});

test("user prompt only", () => {
  const p = ev("session.inbox.enqueued", { item: { type: "user", payload: { text: "fix it" } } });
  assert.deepEqual(p, { time: now.toISOString(), project: "game", harness: "opencode", kind: "prompt", prompt_text: "fix it" });
  assert.equal(ev("session.inbox.enqueued", { item: { type: "synthetic" } }), null);
});

test("tools: write/edit/patch vs other", () => {
  assert.deepEqual(
    [ev("session.tool.called", { input: { path: "a", content: "x\ny" } })?.kind, ev("session.tool.called", { input: { path: "a", content: "x\ny" } })?.lines_added],
    ["edit", 2],
  );
  const e = ev("session.tool.called", { input: { oldString: "a", newString: "b\nc" } });
  assert.equal(e?.lines_added, 2);
  assert.equal(e?.lines_removed, 1);
  assert.equal(ev("session.tool.called", { input: { patch: "" } })?.kind, "edit");
  assert.equal(ev("session.tool.called", { input: { command: "ls" } })?.kind, "tool_call");
  assert.equal(ev("session.tool.called", {})?.kind, "tool_call");
});

test("ignored and malformed", () => {
  assert.equal(ev("session.reasoning.delta"), null);
  assert.equal(toEvent(null, "g"), null);
  assert.equal(toEvent({}, "g"), null);
});
