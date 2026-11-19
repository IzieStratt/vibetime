// Browser E2E for the reviewer console: real server binary, real Chromium.
// Usage: node --test e2e/console.test.mjs  (needs ../bin/vibetime-server built: go build -o bin/ ./cmd/...)
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const bin = join(root, "bin", process.platform === "win32" ? "vibetime-server.exe" : "vibetime-server");
const dir = mkdtempSync(join(tmpdir(), "vt-e2e-"));
const db = join(dir, "s.db");
const port = 18000 + Math.floor(Math.random() * 1000);
const base = `http://127.0.0.1:${port}`;
let srv, browser, reviewer, kid, subId;

const call = async (path, tok, body) => {
  const r = await fetch(base + path, { method: body ? "POST" : "GET", headers: { Authorization: "Bearer " + tok, "Content-Type": "application/json" }, body: body && JSON.stringify(body) });
  return r.json();
};

before(async () => {
  reviewer = execFileSync(bin, ["-db", db, "-add-reviewer", "fraudsquad"]).toString().trim();
  srv = spawn(bin, ["-db", db, "-addr", `127.0.0.1:${port}`, "-open-signup", "-audit-rate", "1"], { stdio: "ignore" });
  for (let i = 0; i < 50; i++) { try { await fetch(base + "/healthz"); break; } catch { await new Promise(r => setTimeout(r, 100)); } }
  kid = (await (await fetch(base + "/api/v1/signup", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ handle: "rowan" }) })).json()).token;
  // a never-synced bundle: valid chain, but unanchored, so it lands in the queue
  const now = Date.now(), links = [];
  let prev = "0".repeat(64);
  const { createHash } = await import("node:crypto");
  for (let i = 0; i < 5; i++) {
    const ev = { time: new Date(now - (10 - i) * 60000).toISOString(), kind: "prompt", project: "cs2", prompt_text: "please make the bomb timer beep faster near the end " + i };
    // mirror chain.Next: sha256(prev + JSON(redacted event))
    const red = { time: ev.time, kind: ev.kind, project: ev.project, prompt_sha256: createHash("sha256").update(ev.prompt_text).digest("hex"), prompt_len: ev.prompt_text.length };
    prev = createHash("sha256").update(prev + JSON.stringify(red)).digest("hex");
    links.push({ event: ev, hash: prev });
  }
  const sub = await call("/api/v1/submissions", kid, { device: "device-e2e-01", project: "cs2", repo: "", from: new Date(now - 3600000).toISOString(), to: new Date(now).toISOString(), prev: "0".repeat(64), links });
  assert.equal(sub.status, "needs_review", JSON.stringify(sub));
  assert.equal(sub.result.valid, true, "JS chain must match Go chain");
  subId = sub.id;
  browser = await chromium.launch();
});

after(async () => { await browser?.close(); srv?.kill(); });

test("reviewer signs in, inspects, and partially approves", async () => {
  const page = await browser.newPage();
  const errors = []; page.on("pageerror", e => errors.push(e.message));
  await page.goto(base + "/review");
  await page.fill("#tok", "wrong");
  await page.click("#go");
  await page.waitForSelector("#lerr:not(:empty)");
  await page.fill("#tok", reviewer);
  await page.click("#go");
  await page.waitForSelector("#queue button");
  assert.match(await page.textContent("#queue"), /rowan \/ cs2/);
  await page.waitForSelector("#detail h2");
  const detail = await page.textContent("#detail");
  assert.match(detail, /unanchored/);
  assert.match(detail, /bomb timer/, "shared prompts are shown to reviewers");
  assert.ok(await page.$("#detail svg rect"), "timeline rendered");
  await page.fill('input[aria-label="Hours to approve"]', "0");
  await page.fill('input[aria-label="Note"]', "never synced live");
  await page.click("text=Approve hours");
  await page.waitForSelector("text=Queue is empty.");
  const sub = await call("/api/v1/submissions/" + subId, reviewer);
  assert.equal(sub.status, "partial");
  assert.equal(sub.approved_seconds, 0);
  assert.equal(sub.note, "never synced live");
  await page.setViewportSize({ width: 375, height: 800 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= 375), "no horizontal scroll on phones");
  assert.deepEqual(errors, []);
});

test("kid cannot use the console", async () => {
  const r = await fetch(base + "/api/v1/review/queue", { headers: { Authorization: "Bearer " + kid } });
  assert.equal(r.status, 403);
});

test("public profile shows insights and updates live", async () => {
  const sync = mins => fetch(base + "/api/v1/sync", { method: "POST", headers: { Authorization: "Bearer " + kid, "Content-Type": "application/json" },
    body: JSON.stringify({ device: "device-e2e-01", head: "a".repeat(64), minutes: mins }) });
  const minute = Math.floor(Date.now() / 60000) * 60;
  const mins = Array.from({ length: 12 }, (_, i) => ({ project: "cs2", minute: minute - (i + 1) * 60, seconds: 60, prompts: 1, ai_lines: 5 }));
  mins[0].human_edits = 3;
  await sync(mins);
  await fetch(base + "/api/v1/import", { method: "POST", headers: { Authorization: "Bearer " + kid, "Content-Type": "application/json" },
    body: JSON.stringify({ source: "hackatime", days: [{ date: "2026-01-05", project: "old-game", seconds: 7200 }] }) });
  const page = await browser.newPage();
  const errors = []; page.on("pageerror", e => errors.push(e.message));
  await page.goto(base + "/u/rowan");
  await page.waitForFunction(() => document.getElementById("today").textContent === "12m");
  assert.equal(await page.textContent("#prompts"), "12");
  assert.equal(await page.textContent("#human"), "3");
  assert.equal(await page.textContent("#ai"), "60");
  assert.equal(await page.locator("#bars rect").count(), 30);
  assert.match(await page.textContent("#projects"), /cs2/);
  assert.equal(await page.isVisible("#imp"), true);
  assert.match(await page.textContent("#imported"), /old-game\s*2h 0m/);
  assert.doesNotMatch(await page.textContent("#projects"), /old-game/, "imported time is kept apart");
  // live: a new sync updates the page without reloading
  await sync([{ project: "cs2", minute, seconds: 60, prompts: 1 }]);
  await page.waitForFunction(() => document.getElementById("today").textContent === "13m", null, { timeout: 5000 });
  assert.equal(await page.textContent("#prompts"), "13");
  await page.setViewportSize({ width: 375, height: 800 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= 375), "no horizontal scroll on phones");
  assert.deepEqual(errors, []);
});
