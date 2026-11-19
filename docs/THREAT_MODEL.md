# vibetime threat model and review workflow

## Ground truth

**The client can't be trusted.** vibetime is AGPL open source and runs on the user's machine, so anyone can rebuild the agent to log whatever they like. No local signature, obfuscation or "anti-cheat" changes that. Our goals are:

1. Make faking hours **cost more effort than doing the work**.
2. Anchor claims to **evidence the user doesn't control**: our server's receive times, GitHub's push times, and the shipped code itself.
3. Make review **fast, and focused on the risky hours**, so Fraud Squad isn't hand-checking 123k hours.

An hour submitted through vibetime is a claim with evidence attached. It isn't proof.

## How programs and Fraud Squad use it (no tooling of their own)

Programs don't build anything. The server provides:

- **Submissions API**: `POST /v1/submissions {project, repo, from, to}` returns a submission ID, the claimed hours, a **confidence breakdown** and **flags**. YSWS forms and the Unified Projects DB call it with the user's vibetime handle and the repo URL. A webhook fires when the review status changes.
- **Reviewer console**: a timeline per submission (attended minutes, prompts, AI vs human edits, commits, idle gaps), with the evidence bundle (transcripts, diff stats, optional timelapse) inline. Each flag links to the minutes it covers.
- **Risk-based triage**: each submission gets a score. Low-risk hours are auto-approved, and a random 5% sample of them is still hand-checked to keep the scoring honest. Medium-risk submissions get a quick review, and high-risk ones get Fraud Squad. Reviewer decisions feed back into calibrating the flags.
- **Partial approval** as a first-class action: approve 31 of 40 hours with a reason. This is what Stardance already does informally ("deflate").
- **Export**: CSV/JSON, plus the Unified Projects DB fields (`hours`, `hours_approved`, `review_url`), so programs keep their existing Airtable flow.

Fraud Squad gets cross-user views: identical transcripts or diffs across accounts, shared device fingerprints, and statistical outliers (hours/day, prompt cadence entropy).

## Attacks against the current design

Status: **Fixed** = shipped with tests. **Mitigated** = planned or partial. **Open** = no good answer yet.

### A. Lying to the local agent

| # | Attack | Status / defense |
|---|---|---|
| A1 | **Script fake events**: `curl` a prompt to localhost every 4 min and get 100% credit. Trivial today. | **Open at the event level.** Event counts alone are worthless as proof. Defenses are server side: hours only count if the submission's diffs and commits are consistent with the claimed activity (C1, C2), and transcripts must be coherent with the code. A bot posting "prompt" with no resulting code gets flagged as *prompts without output*. |
| A2 | **Backfill**: POST events with timestamps from last month. | **Fixed.** The agent drops events more than 5 min old or 1 min in the future, for both native events and WakaTime heartbeats (`internal/api/guard.go`, tested). Remaining gap until server commitments ship: a patched agent ignores this, which is why D1 exists. |
| A3 | **Malicious website injects events** via CSRF to `127.0.0.1`, or reads data via DNS rebinding. | **Fixed.** Host must be loopback, cross-site `Origin` is rejected, and POST requires `application/json`, so the browser has to send a CORS preflight that we never approve. All of this is tested. |
| A4 | **Patched agent**: rebuild vibetime to fabricate a perfect timeline. | **Open by nature.** It's caught only through server-side evidence: D1 timing, C1/C2 code consistency and transcript review. |
| A5 | **Spool/DB editing**: edit `spool.jsonl` or `events.db`. | The hash chain detects edits to stored history (tested). The spool is pre-chain, but stale timestamps get dropped on flush (A2). |

### B. Real tools, fake effort (the hard part)

| # | Attack | Status / defense |
|---|---|---|
| B1 | **"continue" farming**: send a one-word prompt every <5 min while watching YouTube. Every minute is credited through the watch allowance. | **Mitigated (planned).** The watch allowance applies only if presence shows input, and repeated low-content prompts decay it (the 3rd "continue" in a row earns 1 min, not 5). Low prompt entropy also becomes a reviewer flag. |
| B2 | **Mouse jiggler / auto-clicker** defeats the idle detector. | **Mitigated (planned).** Presence is never sufficient on its own; it only extends credit that real harness or editor events started. Jiggler input with no focus changes, keystrokes or edits shows up as a pattern: periodic, zero-variance intervals. |
| B3 | **Agent driving the agent**: a script or second LLM writes prompts to Claude Code all day. The hooks are genuine. | **Open; the biggest long-term risk.** Signals: inhumanly regular cadence, prompts written faster than typing speed (prompt length ÷ time since last stop), no human edits ever, no idle or sleep pattern, 16 h days. These are flags for human review, not proof. |
| B4 | **Parallel sessions**: run 3 terminals on 3 projects, or 3 machines on one project. | Same-device time is merged into one timeline, so it's never double counted (tested). **Cross-device merging per user** is needed server side, and it's planned for M2. |
| B5 | **Padding the struggle score**: long prompts with fake code references, pointless manual edits. | The score is advice shown to users, never an approval gate. Reviewers judge the shipped artifact. |
| B6 | **Timelapse fakes**: looped frames, AI-generated or someone else's screen. | Frames are hash-committed live (D1), so a timelapse can't be produced after the fact. Loop detection uses perceptual hashes. It can't prove *who* is at the keyboard. |

### C. Lying about *what* was built

| # | Attack | Status / defense |
|---|---|---|
| C1 | **Project laundering**: track time in repo X, then rename the folder to claim it for submission Y. | Projects bind to the **git remote and root commit**, not the folder name. A submission must match the repo URL, and commits in the window must exist on GitHub. |
| C2 | **Copy someone else's repo** and fake activity over it. | Submitted edit events must roughly reconcile with the repo's diffs over the window (lines changed, files touched). Pushed-commit timestamps come from **GitHub's event API**, which the user can't control. Commit author dates are forgeable, so they're ignored. |
| C3 | **Redact everything** in the evidence bundle. | Redaction is allowed but visible, and heavy redaction lowers confidence, so it means slower review, not rejection. |
| C4 | **Hackatime import** of old, unverifiable hours. | Imported hours are labeled *unverified* and kept separate from vibetime-attested hours. |

### D. Server-side anchors (what makes the client's lies expensive)

| # | Defense | Status |
|---|---|---|
| D1 | **Live commitments**: the agent sends its chain head every minute, and the server records the receive time. A submitted bundle must replay to those exact heads, so a session must be faked *in real time* and can't be generated later. | Planned (M3). |
| D2 | **Cross-device merge** per account (B4). | Planned (M2). |
| D3 | **Population statistics**: outlier hours, cadence entropy, identical transcripts across accounts. | Planned (M3+). |

## What we accept

A determined cheater who runs a bot in real time for 40 hours, writes coherent prompts, and ships a real project whose diffs match can probably get through. At that point they've spent the hours supervising real AI-assisted development of a shipped project, which is close to what the program wants anyway. The aim is to make cheating more work than the honest path, not impossible.
