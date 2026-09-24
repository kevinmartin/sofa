# Milestone 09: Evaluated dreaming and improvement proposals

## Outcome and prerequisites

A bounded scheduled loop learns from completed work and later feedback, producing reviewable, evidence-linked improvements to knowledge, skills, tests, or recipes. It spends no inference when nothing eligible changed, measures its own overhead, and cannot promote its own instructions.

Depends on milestones **07 and 08**. Read [the shared completion contract](README.md), [the main plan](../../PLAN.md), [the memory design](../memory.md), and [runtime/watch contracts](../runtime-and-watches.md). Reuse memory event cursors, the watch registry, standing grants, cumulative budgets, and trusted publication. Use existing ACP/System One profiles rather than introducing another agent runtime.

## Scope and exclusions

Implement deterministic eligibility, preparation, resumable batches, optional System One organization, bounded synthesis, evaluation, and proposal publication in Go. Agents supply interpretations and proposed changes; Go controls side effects and promotion boundaries.

Allow ordinary permitted knowledge/skill/test/recipe PRs. Protected workflow, credential, authorization, gate-threshold, and provider-routing changes remain patch/specification proposals for owner handling. Exclude autonomous merges, production releases, cross-repository export, automatic policy changes, and claims that an optimization is proven merely because a model recommends it.

## Checkpoints

1. **Eligibility and scheduling.** Add weekly/manual dreaming to the shared ticker. Process new outcome and correction event revisions, including feedback, merges, reverts, and release observations affecting previously consumed episodes. Exclude dreaming/evaluation housekeeping by default, preventing recursive eligibility. Empty batches exit without inference. Delivery has priority.
2. **Durable bounded batches.** Persist selected event IDs, affected episodes, cursor, phase, fingerprints, counters, and proposal IDs. Defaults: one active run, at most 50 affected episodes, 30 minutes per execution, at most three proposals, and one open consolidation PR. Commit processing results before advancing the cursor. Resume interrupted work without duplicate PRs or resetting cumulative inference/evaluation budgets; over-budget work defers explicitly.
3. **Preparation and synthesis.** Deterministically join outcomes, exact-head validations, owner feedback, cost/usage, retrieval records, repeated failure fingerprints, and reverts. Optional Jev organizes an allowed shortlist. A configured ACP agent proposes corrections or reusable procedures with supporting and contradicting evidence. Missing/expired artifacts reduce claims; hypotheses never become facts by repetition.
4. **Evaluation and review.** Run cheap deterministic safety/retrieval fixtures first. Compare no-memory, current-memory, and candidate variants on representative held-out cases not used for synthesis. Pin versions and repeat stochastic comparisons where needed. Larger replay evaluations are checkpointed child work with explicit aggregate budgets; exhausting the execution window leaves evaluation pending. Publish a bounded draft PR with measured outcomes, uncertainty, costs, and rollback; readiness requires the applicable independent gates. Kevin merges.
5. **Follow-through.** Track adoption and subsequent outcomes using new events. Generate corrections or rollback proposals for regressions. Include observation, dreaming, replay, runner, and provider usage in savings reports. Unknown cost remains unknown; insufficient data produces an experimental proposal rather than a claimed improvement.

## Observable acceptance

- [ ] **09-A:** Empty eligible state and repeated completed delivery call neither System One nor an LLM; dreaming/evaluation events alone do not schedule more inference.
- [ ] **09-B:** A later feedback or revert event revisits an old episode exactly once even after its initial cursor has advanced.
- [ ] **09-C:** Cancel/retry at preparation, synthesis, evaluation, and publication boundaries preserves counters and one proposal identity. More than 50 affected episodes remain queued rather than skipped.
- [ ] **09-D:** Budget exhaustion and delivery contention defer work without silently raising limits; partial evaluations remain visibly incomplete.
- [ ] **09-E:** Evaluation fixtures detect stale/conflicting guidance, poisoned hypotheses, negative examples, scope leakage, and gate weakening. Candidate synthesis cannot read held-out answers.
- [ ] **09-F:** Reports compare current/candidate/no-memory outcomes and total overhead; an unfavorable or uncertain result cannot be relabeled proven success.
- [ ] **09-G:** Protected changes become owner-handled proposals, while permitted changes follow normal PR publication. Candidate skills cannot activate before reviewed promotion.
- [ ] **09-H:** In an authorized disposable consumer repository, a configured live ACP profile processes clearly labeled scenario outcomes and opens one permitted draft consolidation PR. Repeating the dispatch creates no duplicate; unchanged subsequent input uses zero inference. No merge is required or performed by the agent.

## Evidence and handoff

During implementation, write `docs/milestones/evidence/09-dreaming.md` with source/event IDs, fixtures, versions, evaluation splits, usage, uncertainty, hosted runs, PR link, interruption checks, and limitations. Live repository/profile credentials are required prerequisites for live acceptance, not grounds to silently waive it. Do not create a fictional completion report now.

Hand milestone 10 the canary scenario, reset/rollback instructions, immutable evaluation inputs, public-output checks, and evidence that reviewed promotion remains independent of dreaming.

## Goal prompt

```text
Implement milestone 09 from docs/milestones/09-dreaming.md and follow docs/milestones/README.md. Verify prerequisites, implement the bounded dreaming and evaluation flow, complete local/replay and required live acceptance, and record actual evidence in docs/milestones/evidence/09-dreaming.md. Preserve cumulative budgets, privacy, and owner review. Report missing external prerequisites explicitly; do not waive checks, auto-merge, or release to production.
```
