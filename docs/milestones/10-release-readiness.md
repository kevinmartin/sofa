# Milestone 10: Integrated release readiness

## Outcome and prerequisites

Produce a verified release candidate and an evidence-backed handoff for Kevin to promote. Completion means the candidate passes required integration acceptance, public/private canaries, compatibility checks and rollback rehearsal. It does not mean publishing a stable release, moving public `v1`, merging work or deploying production.

Depends on **all milestones 01, 01.1, and 02–09** and their evidence reports. Read the [shared milestone contract](README.md), [main plan](../../PLAN.md), [lifecycle](../lifecycle.md), [runtime/watch design](../runtime-and-watches.md), [memory](../memory.md), and [System One design](../system-one.md). Reconcile implementation against these contracts before assembling the candidate; an earlier milestone's unchecked live prerequisite remains unchecked here.

## Scope and exclusions

Integrate the Go CLI, reusable workflows/actions, supported ACP/provider combinations, independent gates, System One decisions, memory, watches and dreaming. Finish installation, upgrade, recovery and troubleshooting documentation. Revalidate current external compatibility and runtime constraints where the implementation depends on them; planning-time documentation is not runtime evidence.

Bind every candidate binary/image/workflow to one immutable source commit and record dependency/harness versions and artifact digests. Internal action/binary references must resolve to that tested release revision. Produce proposed release notes, compatibility status and owner promotion instructions. Protect the existing stable channel; use explicitly authorized disposable fixtures and an isolated canary channel for movement tests.

Exclude new features, unsupported matrix combinations, expanded authentication scope, automatic stable promotion, consumer production enrollment and production deployment. Do not use the milestone as blanket permission to create external repositories, broaden secrets or modify the user's stable release tags.

## Implementation checkpoints

1. **Candidate audit:** map every main-plan acceptance scenario to executable verification or a justified documented exclusion already allowed by the plan. Review unresolved evidence, secret boundaries, pinned dependencies and migration compatibility. Fix integration defects without relaxing gates or silently shrinking advertised support.
2. **Integrated local suite:** exercise lifecycle, provider adapters, routing, blackbox/security gate control, memory and watches together. Inject interrupted dispatch/publication, stale workers, revoked grants, expired evidence and poisoned inputs. Assert zero inference for idle/duplicate/recipe paths and persistent aggregate budgets.
3. **Live canaries:** use authorized public and private consumer fixtures with separate caller-owned credentials. Exercise approved specification/Ready delivery, required validation, a granted watch repair and reviewed-learning proposals. Complete the supported live ACP/provider matrix; preserve unsupported/experimental labels. Observe an owner-authorized merge/release fixture where needed, without autonomously merging to obtain evidence.
4. **Upgrade and rollback:** run both consumers against a canary major reference, advance that isolated reference to a compatible candidate, then rerun without changing caller files. Rehearse restoring the previous compatible candidate, including state/checkpoint compatibility. Record explicit recovery handling for incompatible persisted state; never reset counters or erase evidence to simulate success.
5. **Release handoff:** assemble reproducible artifacts, final evidence and precise promotion/rollback instructions. Report actionable limitations and measured cost/latency, separating inference, setup, validation, observation and learning overhead. Stable promotion remains Kevin's subsequent action.

## Observable acceptance

- [ ] **10-A:** Every advertised feature and supported ACP/provider profile has current evidence bound to the final candidate commit; later code changes invalidate affected checks.
- [ ] **10-B:** Public/private canaries complete admission, isolated execution, current-candidate gates and one draft PR without credential crossover or duplicate publication.
- [ ] **10-C:** Unauthorized callers, malicious source/configuration/artifacts and poisoned memory cannot obtain Projects, publisher or unrelated provider credentials.
- [ ] **10-D:** Secretless product execution remains isolated from specialist credentials; endpoint changes cannot forward native subscription credentials.
- [ ] **10-E:** Required blackbox/security roles fail closed on missing or stale evidence; failed release observation cannot become Done.
- [ ] **10-F:** Interruption, conflicting writes, artifact expiry, revoked grants, quotas and duplicate events preserve state ownership and cumulative budgets.
- [ ] **10-G:** Watch/dreaming authority remains bounded, sensitive reporting remains private, and proposals cannot silently become active policy.
- [ ] **10-H:** Both consumers receive a compatible canary update without copied implementation changes; exact-SHA consumption and rollback also work.
- [ ] **10-I:** Installation diagnostics, operator recovery instructions and release manifests match observed behavior; unknown costs remain explicitly unknown.
- [ ] **10-J:** The handoff identifies the immutable candidate, all required passed checks, release risks, and owner-controlled promotion steps; public stable references remain unchanged.

## Evidence, handoff and goal

When implemented, write `docs/milestones/evidence/10-release-readiness.md`. Include prerequisite evidence links, candidate commit/digests, commands/results, sanitized live run/PR links, matrix coverage, both upgrade/rollback traces, remaining limitations and the promotion checklist. Do not create placeholder evidence now.

If an external environment, credential or owner action is unavailable, complete independent local work and list the exact unfinished live check. A local pass cannot substitute for public/private canaries or real supported-agent verification. Keep release readiness explicitly incomplete until required evidence exists; missing authorization is never inferred from elapsed time.

```text
Implement and verify milestone 10 in docs/milestones/10-release-readiness.md under docs/milestones/README.md. Integrate completed milestones 01, 01.1, and 02–09, produce an immutable release candidate, run required authorized live canaries and isolated upgrade/rollback rehearsals, and write the specified evidence report. Completion is a verified release-ready handoff. Do not merge, deploy production, publish stable releases or move public v1. Preserve incomplete live checks honestly and continue independent local work when external prerequisites are missing.
```
