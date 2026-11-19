# vibetime threat model and review workflow

## Ground truth

**The client can't be trusted.** vibetime is AGPL open source and runs on the user's machine, so anyone can rebuild the agent to log whatever they like. No local signature, obfuscation or "anti-cheat" changes that. Our goals are:

1. Make faking hours **cost more effort than doing the work**.
2. Anchor claims to **evidence the user doesn't control**: our server's receive times, GitHub's push times, and the shipped code itself.
3. Make review **fast, and focused on the risky hours**, so Fraud Squad isn't hand-checking 123k hours.

An hour submitted through vibetime is a claim with evidence attached. It isn't proof.

## How programs and Fraud Squad use it (no tooling of their own)

Programs don't build anything. The server provides:

- **Submissions API**: `POST /api/v1/submissions` (sent by `vibetime submit`) returns a submission ID, the claimed hours, a **confidence breakdown** and **flags**. YSWS forms and the Unified Projects DB call it with the user's vibetime handle and the repo URL. A webhook fires when the review status changes.
- **Reviewer console**: a timeline per submission (attended minutes, prompts, AI vs human edits, commits, idle gaps), with the evidence bundle (transcripts, diff stats, optional timelapse) inline. Each flag links to the minutes it covers.
- **Risk-based triage**: each submission gets a score. Low-risk hours are auto-approved, and a random 5% sample of them is still hand-checked to keep the scoring honest. Medium-risk submissions get a quick review, and high-risk ones get Fraud Squad. Reviewer decisions feed back into calibrating the flags.
- **Partial approval** as a first-class action: approve 31 of 40 hours with a reason. This is what Stardance already does informally ("deflate").
- **Export**: CSV/JSON, plus the Unified Projects DB fields (`hours`, `hours_approved`, `review_url`), so programs keep their existing Airtable flow.

Fraud Squad gets cross-user views: identical transcripts or diffs across accounts, shared device fingerprints, and statistical outliers (hours/day, prompt cadence entropy).

## Attacks and how each is handled

Every row names the code that handles it and the test that attacks it. **Blocked** = the attack doesn't work. **Flagged** = it can't be stopped on an untrusted client, so it's detected and sent to a human with the evidence. Flags carry weights, and any non-zero risk means human review.

### A. Lying to the local agent

| # | Attack | Handling | Code / test |
|---|---|---|---|
| A1 | Script `curl`s fake prompts to localhost | **Flagged**: `prompts_without_output` (prompts that changed nothing), `robotic_cadence` (prompt intervals with CV < 0.2) | `verify.behaviour` / `TestA1ScriptedPrompts` |
| A2 | Backfill old timestamps | **Blocked** twice: the agent drops events more than 5 min off its clock, and the server only accepts minutes from the last 15 min. Submitted events must be anchored by a live commitment received within 3 min, or they're `backfilled` and don't count | `api/guard.go`, `server.sync`, `verify.within` / `TestStaleAndFutureEventsDropped`, `TestSyncRejectsBackfillAndClamps`, `TestA2BackfillCaught` |
| A3 | Malicious website: CSRF or DNS rebinding against the agent | **Blocked**: Host must be loopback, cross-site `Origin` is rejected, and POST must be JSON (which forces a preflight that is never approved) | `api/guard.go` / `TestGuardBlocksBrowserAttacks` |
| A4 | Patched agent fabricates a timeline offline | **Blocked**: hours only count when events are covered by chain heads the server received *at the time*. A never-synced bundle verifies to 0 h (`unanchored`) | `verify.Verify` / `TestA4NeverCommitted`, `TestFabricatedBundleRejected` |
| A5 | Edit the DB, spool or bundle | **Blocked**: any edit breaks the hash chain (`chain_broken`, 0 h), and stale spooled events are dropped | `chain`, `verify` / `TestA5TamperedBundle`, `TestDBTamperDetected` |
| D1 | Submit a different or partial timeline than the one committed live | **Flagged**: `chain_gap` (commitments in the window that are missing from the bundle) | `TestChainGap` |

### B. Real tools, fake effort

| # | Attack | Handling | Code / test |
|---|---|---|---|
| B1 | "continue" every 4 min while away | **Blocked + flagged**: after the first short prompt (<20 chars), repeats only earn a 1-min watch allowance, not 5 (works on redacted prompts via `prompt_len`). `low_effort_prompts` flag | `attend` / `TestContinueFarmingDecays`, `TestB1LowEffortFarming` |
| B2 | Mouse jiggler | **Blocked**: presence never earns time, it can only end it (`Idle`). Hours with no idle at all are flagged `no_presence_signal` | `presence`, `verify` / `TestB2NoPresence` |
| B3 | Second LLM writing the prompts | **Flagged**: `superhuman_typing` (>15 chars/s since the agent stopped), `robotic_cadence`, `long_day` (>14 h in any sliding 24 h), `no_presence_signal`, `no_human_activity` | `TestB3AgentDrivingAgent` |
| B4 | Parallel sessions or devices | **Blocked**: a single device keeps one merged timeline, and the server caps each user at 60 s per minute across all devices | `attend`, `server.UserStats` / `TestInterleavedNeverExceedsWallClock`, `TestB4SameDeviceParallel`, `TestCrossDeviceMerge` |
| B5 | Padding the struggle score | **Not exploitable**: the score is advice and never an approval input. Reviewers judge the shipped work | design |
| B6 | Faked timelapse | **Not applicable yet**: there's no timelapse. When it's added, frame hashes go into the chain like any other event, so they're anchored by D1 | planned |

### C. Lying about what was built

| # | Attack | Handling | Code / test |
|---|---|---|---|
| C1 | Time logged in repo X submitted for Y | **Flagged**: every event carries its normalized git remote, and a mismatch raises `repo_mismatch` | `hooks.RepoFor`, `verify.repoChecks` / `TestC1Laundering` |
| C2 | Copied repo / no real output | **Flagged**: GitHub's *own* push timestamps (activity API, not forgeable commit dates). `no_pushes` in the window, `lines_mismatch` (>10x off) | `gitev`, `verify` / `TestCommits`, `TestC2GitEvidence` |
| C3 | Redact everything | **Neutral by design**: redaction is the privacy default, so it's informational only. Every detector works on prompt length and hashes, and redaction can't change the hash because the chain commits to sha256(prompt) | `event.Redacted` / `TestRedactionPreservesHash`, `TestC3HeavyRedaction` |
| C4 | Import unverifiable Hackatime hours | **Blocked**: imports go to a separate `imported` table, shown as unverified history, never in stats, insights totals or submissions | `server/importer.go` / `TestImportHackatime`, `TestImportEndpointAndInsights` |
| D3 | Same bot or prompts shared across accounts | **Flagged**: `duplicate_prompts` when a distinctive prompt (40+ chars) hash appears in another account's submission | `server.submit` / `TestDuplicatePromptsAcrossAccounts` |

### Review backstops

- **Random audit**: 5% of clean submissions still go to a human, so the detectors stay honest (`TestRandomAudit`).
- **Partial approval**: reviewers can approve between 0 and the verified seconds, never more (`TestReviewFlow`).
- Nobody can review their own submission. Submissions are private to the owner and reviewers.

## What we accept

A determined cheater who runs a bot in real time for 40 hours, writes coherent prompts, and ships a real project whose diffs match can probably get through. At that point they've spent the hours supervising real AI-assisted development of a shipped project, which is close to what the program wants anyway. The aim is to make cheating more work than the honest path, not impossible.
