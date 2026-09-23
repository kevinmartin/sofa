# 02 — Automate Discovery and lifecycle handoffs

## Outcome

An admitted idea can become a researched draft specification, receive Kevin's approval, enter Backlog, and proceed through the delivery lifecycle when Kevin marks it Ready. Board state reflects verified work, review feedback, and release evidence rather than an agent's unsupported completion claim.

Requires [01](01-delivery-slice.md). Read its evidence report, [the shared contract](README.md), [PLAN.md](../../PLAN.md), and [lifecycle](../lifecycle.md). Reuse 01's approval/state contracts rather than introducing a second admission mechanism.

## Prerequisites

Local tests use replayed Project/issue/run events. Live checks need the designated consumer Project, configured status mapping and owner identity, its existing profile, and permission to update only the agreed test items. Release observation needs an owner-merged canary or another designated already-merged test PR; the implementing agent must not merge to satisfy the check. This fixture may be independent of sofa's generated candidate, so milestone 02 does not require merging an incompletely validated prototype before 04 exists.

## Scope

- Implement Inbox → Discovery → Spec Review → Backlog → Ready → Building → Verification → Review → Release → Done using the built-in Project Status field, configurable display names, and Product/Delivery views or documented setup. Keep blocked reason and next action separate from lifecycle position.
- Add explicit owner admission for Discovery, with two active discovery items by default. Public issue creation alone does not authorize inference. A later standing discovery-watch grant will reuse the admission interface.
- Generate a versioned specification with evidence, goals/non-goals, constraints, dependencies, acceptance examples, relevant risks, and proposed validation. Deterministic repository facts and duplicate identifiers are gathered first.
- Present the versioned specification for Kevin to review. His move from Spec Review to Backlog approves the revision captured at that transition; sofa computes and stores its digest internally, without a comment command. Restrict Project write access and keep factory automation from setting Backlog or Ready. Material changes return to Spec Review; formatting normalization cannot conceal changed acceptance criteria.
- Expand targeted reconciliation into per-repository Project polling with one ticker, a ten-minute default and hourly economy preset, manual/event wakeups, and due-work coalescing. Polling must not require a model or repository build.
- Track dependencies and changed/stale backlog inputs, prioritizing explicitly configured owner fields. Suggest revisions or re-review rather than rewriting every specification on every tick.
- Synchronize real worker/PR/check outcomes and owner review feedback. Only authorized feedback can initiate bounded repair of an admitted task; outside comments remain untrusted input. Closing without merge is a distinct outcome.
- Observe configured post-merge release/default-branch acceptance and smoke checks, bound to the actual merged/released commit. Record later feedback, merge, release, and revert events for future learning. No new deployment or rollback authority is introduced.

Full required gate implementations arrive in 04. This milestone consumes the gate-result contract: in live use, absent required gates keep work in Verification/draft. Replay tests can supply explicit fake results, clearly labeled as fixtures. Do not auto-mark a prototype PR ready to demonstrate the board.

Exclude Linear, draft/PR-card intake, automated prioritization by models, standing maintenance grants, and new runtime backends.

## Checkpoints

1. **Transitions:** implement and replay the state machine, approval rules, revocation, source revision checks, and projection reconciliation.
2. **Discovery:** produce/revise a real specification and exercise owner approval against the canary Project.
3. **Scheduling and feedback:** add broad polling, budget/WIP handling, stale-item detection, and bounded authorized review repair.
4. **Completion observation:** validate merge/release outcomes and append correction events; document board setup and recovery.

## Acceptance

- [ ] **02-A:** Live Discovery produces a specification with evidence and acceptance criteria. Before the trusted Project moves it from Spec Review to Backlog, it stays out of Backlog. Sofa binds the exact revision internally; this move does not start implementation.
- [ ] **02-B:** Current Ready in the restricted Project admits the frozen, Backlog-approved scope. A changed revision or material edit cannot silently inherit that approval; Project write access and automations must be configured so untrusted actors cannot set approval states. Default WIP and aggregate budgets hold across split tasks.
- [ ] **02-C:** Table-driven/replay tests cover every state and blocked/deferred/cancelled/superseded outcome, including late events and human changes. Reconciliation does not repeatedly fight unexpected manual board edits.
- [ ] **02-D:** Empty/duplicate polling and no-change backlog grooming make zero model calls. Missed ticks coalesce without dispatch storms; manual wakeup uses the same ledger rules.
- [ ] **02-E:** Valid owner feedback repairs the same PR within the remaining budget; unrelated/untrusted comments cannot expand scope or fetch stronger credentials.
- [ ] **02-F:** Missing required gate evidence leaves the candidate blocked/draft. Stale results from another candidate cannot advance Verification.
- [ ] **02-G:** Live observation of an owner-merged/designated canary distinguishes merge, failed release, successful release, and closed-unmerged behavior as applicable. Use replay fixtures for remaining adverse cases; merge alone does not satisfy a configured release gate.
- [ ] **02-H:** Later feedback/reverts append linked events even after an earlier completion was recorded; no raw sensitive details are projected into public state.

## Evidence and handoff

Write `docs/milestones/evidence/02-lifecycle.md`, including status mapping, transition/approval fixtures, live canary references, ticker behavior, and the gate-contract dependency. Missing owner approval or an appropriate merged fixture is a remaining live prerequisite, not permission for the agent to supply that human action itself.

## Goal prompt

```text
Implement milestone 02 in docs/milestones/02-lifecycle.md, following docs/milestones/README.md and PLAN.md. Build on milestone 01 to add Discovery, revision-bound approval through the trusted Project, lifecycle reconciliation, authorized review feedback, and release observation. Preserve draft/blocked status when required gates are unavailable. Use subagents for bounded independent work and maintain docs/milestones/evidence/02-lifecycle.md with local and live acceptance evidence. Continue useful work around missing prerequisites, but do not claim completion without required evidence. Stop at this milestone; Kevin remains the approval and merge authority.
```
