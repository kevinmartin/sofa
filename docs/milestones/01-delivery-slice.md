# 01 — Prove a safe hosted delivery slice

## Outcome

In an owner-designated disposable GitHub repository, one reviewed issue moved to Ready in a private, access-controlled Project produces exactly one draft PR through Copilot ACP. The candidate passes the fixture's deterministic checks, privileged credentials remain outside agent execution, and a killed/repeated run recovers without duplicating publication or overwriting human changes.

This is a narrow prototype. It remains draft-only until later milestones provide the full independent gate system; no unimplemented check is reported as passed.

Read [the shared contract](README.md), [PLAN.md](../../PLAN.md), [lifecycle](../lifecycle.md), and [runtime/recovery](../runtime-and-watches.md). No previous milestone is required.

## Prerequisites

Local development can start immediately. Hosted acceptance needs a confirmed sofa GitHub/module identity, a place to publish the candidate toolkit commit, an owner-designated disposable consumer repository and issue-backed Project, a Copilot profile, Projects access, and a repository-scoped publication credential/App configuration. Use references to configured secrets and a finite live-run budget. Do not create or choose external repositories silently.

## Scope

- Establish the Go module, a small CLI, versioned configuration, and minimal internal contracts for work sources, state, execution, evidence, and publication. Add only interfaces consumed by this slice.
- Implement a targeted manual reconciliation command/workflow for the configured issue. Treat current Ready status in the private Project as the single delivery authorization, with Project write access restricted to trusted people and no automation setting Ready. Freeze the issue's canonical specification digest and Ready field revision internally, reject edits after Ready, and revalidate them before publication. GitHub does not expose a status-change actor for this canary item, so do not claim to verify who moved it. There is no Discovery agent or broad scheduled backlog scan yet.
- Implement the orphan `sofa-state` ledger with optimistic concurrent updates, pending dispatch, fenced claims, run ownership, cumulative limits, and phase checkpoints. Dispatch identifies admitted work; it never creates authority.
- Add a fake ACP peer for deterministic tests and one real Copilot adapter through the selected Caelis SDK. Handle negotiation, streaming, permissions, cancellation, errors, and whole-process cleanup.
- Package reusable reconciliation/work workflows and minimal caller/setup integration. Reference the candidate toolkit by an immutable commit for the demonstration; stable `v1` publication belongs to 10.
- Run the agent in the restricted execution environment. Build/test the candidate without factory/provider secrets, validate the returned patch and evidence in a separate trusted job, and publish a draft branch/PR with a repository-scoped token. The publisher executes no proposed code or hooks.
- Record compact observed stage outcomes, revisions, usage when available, and evidence references from the start. This is an append-only observation contract for 07, not a memory retrieval system.
- Include one repository-configured exact Go recipe/formatter path to demonstrate zero-inference work and its applicability/negative cases.

Exclude automatic Discovery, broad board synchronization, watches, System One, other harnesses, memory search, full specialist orchestration, release observation, and automatic readiness/merge. Documentation explains these prototype limits.

## Checkpoints

1. **Contracts and early integration probe:** inspect the actual workspace; establish the fixture and schemas. Verify read-only Project approval/Ready evidence and Copilot ACP startup as soon as prerequisites exist, so unsupported assumptions surface early.
2. **Replayable core:** demonstrate authorization, claims, duplicate suppression, checkpoints, recipe handling, and budget persistence against fake boundaries.
3. **Hosted path:** wire isolated execution, secretless checks, integrity validation, and draft publication through reusable workflows. Complete the real canary.
4. **Recovery and handoff:** interrupt at defined boundaries, re-deliver work, simulate conflicting human changes, and produce the evidence report and operator instructions.

## Acceptance

- [ ] **01-A:** A real hosted run reads the reviewed issue and private Project Ready status, performs one Copilot-generated fixture change, captures successful deterministic checks for that candidate, and opens one draft PR. Record actual run/PR URLs and tested versions.
- [ ] **01-B:** A non-Ready or ambiguously associated item, edits after Ready, changed admitted specification, foreign repositories, malformed dispatches, and duplicate/completed work cannot start a model or publish. Observe model-call counts; do not infer zero from logs being quiet.
- [ ] **01-C:** Exercise concurrent claims and crash points after admission/before dispatch, after accepted checkpoint, and after PR creation/before ledger acknowledgement. Recovery uses real run/branch/PR state and finds the existing PR. Include at least one actual hosted cancellation/rerun, with the full fault matrix covered locally.
- [ ] **01-D:** A stale owner or unexpected human branch update prevents the next write; no force-push or second PR occurs. Live cancellation races are documented with the next-enforcement-check limit.
- [ ] **01-E:** Untrusted patches, paths/symlinks, artifacts, hooks, or agent instructions cannot access Projects/App/publisher credentials or bypass publication validation. Use inert sentinel credentials and adversarial fixtures, not real-secret exfiltration.
- [ ] **01-F:** Timeout/cancellation terminates the contained agent process; auth failures block, quota exhaustion defers, and retries retain their counters. Missing/expired checkpoints restart safely without implying success.
- [ ] **01-G:** A valid exact recipe completes with zero model calls; invalid preconditions do not run that recipe. Idle/no-op reconciliation is also zero inference.
- [ ] **01-H:** Logs/state/artifacts contain no secret values; checkpoints and observed outcomes are schema-versioned. Installation and recovery instructions reproduce the canary.

## Evidence and handoff

Write `docs/milestones/evidence/01-delivery-slice.md` following the shared contract. Identify the tested toolkit commit, schema boundaries, workflow permissions, consumer fixture, redacted resource references, and known draft-only limitations. Hosted acceptance remains pending if the required target or auth is unavailable. Leave the PR for Kevin; do not merge it or promote a stable toolkit tag.

## Goal prompt

```text
Implement milestone 01 in docs/milestones/01-delivery-slice.md, following docs/milestones/README.md and PLAN.md. Prove the narrow safe hosted delivery loop with one Copilot ACP profile, durable recovery, zero-inference recipe/no-op paths, and validated draft-PR publication. Inspect existing work first, use subagents for independent bounded pieces, and maintain docs/milestones/evidence/01-delivery-slice.md. Complete all required local and live acceptance checks; if an external prerequisite is missing, continue independent work and report the exact remaining requirement without claiming completion. Stop at this milestone; do not implement later goals, merge PRs, or promote a stable release.
```
