# Milestone 08: Bounded maintenance watches

## Outcome and prerequisites

Make each repository observe its own health, investigate useful signals, and open bounded maintenance PRs under explicit standing grants. Idle observation remains deterministic. Kevin merges; watches neither manufacture human Ready transitions nor approve their own expanded scope.

Depends on milestones **02, 04, 05 and 07**. Read the [shared milestone contract](README.md), [main plan](../../PLAN.md), [runtime/watch design](../runtime-and-watches.md), [lifecycle](../lifecycle.md), and [memory](../memory.md). Reuse their admission, gate, recovery and episodic-recording implementations rather than building a second scheduler.

## Scope and exclusions

Implement the common watch registry, trusted per-watch configuration, deterministic sensing, event/timer evaluation, activation decisions, finding deduplication and bounded delivery. Standing grants specify enabled state, change classes, paths, evidence prerequisites, required gates, profiles, expiry and cumulative budgets. Recheck their current authority before publication. Ordinary grants cannot publish protected workflow, credential, admission, gate-threshold or provider-routing changes.

Ship these profiles with the documented presets; onboarding enables only applicable profiles:

| Profile | Activation and output |
| --- | --- |
| Gatewatch | Required-check failure; deterministic recovery first, then evaluated classification and specialist diagnosis. Repair the existing admitted PR or use a specific repair grant. |
| Nightwatch | Freshness checks each tick; nightly scoped probes. Detect missing expected observations even with unchanged source; investigate unexplained signals. |
| Bugwatch | Eligible reports and daily sweep; reproduce existing bugs, skipping duplicate, blocked or already active work. |
| Security/dependency watch | Applicable PR scans, daily advisory refresh and optional weekly investigation. Apply only granted repairs; route sensitive findings privately. |
| Tastewatch | Changed-file review and optional weekly sample. Run mechanical rules first; subjective refactors become proposals. |
| Ratchetwatch | Post-merge measurements and weekly aggregation. Propose bounded tests/measurement improvements; protected threshold changes require owner handling. |

Reuse reconcile, discovery and delivery loops. Register the dreaming integration boundary, but its executor belongs to milestone 09. Exclude a central controller, independent heartbeat service, autonomous merging/deployment, cross-repository writes and unconditional scheduled model calls.

## Implementation checkpoints

1. **Registry and sensing:** implement versioned Go definitions and policy validation. One repository ticker evaluates all due work; existing event/manual entrypoints accelerate reconciliation. Use clock-injected tests for deadlines, disabled watches, evidence freshness and coalesced missed windows.
2. **Admission and delivery:** bind each attempt to grant revision, evidence digest, loop and ownership generation. Route qualifying work through existing gates/publication. Out-of-grant discoveries produce draft specifications for Kevin's approval before Backlog. Revocation or scope change stops subsequent writes.
3. **Noise and budget controls:** fingerprint findings, update existing issues, and skip unchanged inputs. Default to three new findings per watch execution, one open ratchet improvement PR, one repository code-writing attempt, and existing persistent retry limits. Enforce configured backlog/resource caps; foreground delivery and broken gates outrank optional patrols.
4. **Evidence and confidentiality:** record observed outcomes and accepted-finding/repair metrics. Sensitive public-repository findings require a configured private destination; absent one, stop sensitive publication with a non-disclosing status. Never place raw details in public logs, artifacts, state or issues. Document Actions scheduler blindness without claiming self-monitoring solves a complete outage.

## Observable acceptance

- [ ] **08-A:** Empty, unchanged, disabled, unauthorized and duplicate evaluations make zero model calls and create no issue chatter.
- [ ] **08-B:** Every profile has a positive fixture and no-action fixture; elapsed freshness deadlines trigger observation without source changes.
- [ ] **08-C:** A valid grant opens exactly one bounded fix PR without a Ready event; absent, expired, narrowed and revoked grants block publication.
- [ ] **08-D:** Scope expansion becomes Discovery; the watch cannot approve the specification or bypass required specialist evidence.
- [ ] **08-E:** Duplicate reports and delayed/repeated events update one finding/attempt; downtime coalesces patrol windows without replay storms.
- [ ] **08-F:** Worker interruption, retries and child tasks preserve ownership, cumulative spending and repair counters.
- [ ] **08-G:** Backlog/PR caps suppress discretionary investigation while deterministic sensing and required checks continue.
- [ ] **08-H:** Sensitive findings remain private, including error paths; no private destination causes a safe publication blocker.
- [ ] **08-I:** An authorized disposable consumer demonstrates one event-driven repair and one timer-driven finding, with captured inference counts and admission provenance.

## Evidence, handoff and goal

When implemented, write `docs/milestones/evidence/08-watches.md` with code revision, commands/results, profile coverage, sanitized event traces, live run/PR links, resource measurements and remaining limits. Continue local work if GitHub fixtures or credentials are unavailable; mark required live acceptance incomplete and name the missing prerequisite. Do not fabricate evidence or mark the milestone complete until required acceptance passes.

```text
Implement milestone 08 in docs/milestones/08-watches.md under docs/milestones/README.md. Reuse completed prerequisites, implement the bounded watch catalogue, verify every acceptance item, and write the specified evidence report. Preserve standing-grant scope, private reporting, human merge authority and zero-inference idle behavior. Do not implement dreaming or create production schedules. Record unavailable required live checks honestly while completing independent local work.
```
