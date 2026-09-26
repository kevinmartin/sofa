# 04 — Enforce independent validation before readiness

## Outcome

A deterministic gate plan explicitly dispatches independent code review, blackbox acceptance, security, and repository checks. A PR becomes ready for Kevin only when all applicable requirements have valid evidence for the current candidate. Agent opinions alone cannot manufacture a passing test result.

Requires [01](01-delivery-slice.md). Read its execution/publication contracts, [the shared contract](README.md), [PLAN.md](../../PLAN.md), and [lifecycle validation](../lifecycle.md). The tested Copilot profile is sufficient for this milestone; profiles from 03 can be used if already available. Integrate with 02's lifecycle when present without making that a hidden prerequisite.

## Prerequisites

Local gate and fault tests use deterministic fixtures. Live specialist checks need the designated hosted consumer, its allowed model profile, a finite budget, a secretless disposable preview/test environment, and permission to publish/update the scoped canary PR. Include fixtures for web/API, CLI, and consumer-facing library behavior; do not assume every repository is a website.

## Scope

- Build a Go gate planner from trusted repository capability profiles, the approved specification, changed surfaces, and policy. Recompute applicability after patch changes. A model may recommend an additional gate but cannot remove one.
- Implement Passed, Failed, Blocked, and Not Applicable. N/A requires a deterministic policy reason. Missing profiles/environments, crashed specialists, inconclusive results, and expired evidence are Blocked.
- Implement integrity, build/lint/unit/integration checks, independent code review, blackbox acceptance, and security scanning/assessment. Add repository-defined performance, accessibility, and compatibility gate adapters where required by a profile/specification, without inventing universal thresholds.
- Give code reviewers fresh sessions. Give blackbox testers approved behavior, public interfaces, fixtures, and a built artifact/preview, with implementation source and the implementer's reasoning withheld by default. CLI/library tests use consumer fixtures rather than a mandatory browser.
- Run security scanners for applicable code; dispatch a separate security specialist for configured sensitive surfaces such as auth, permissions, external input, dependencies, and secrets. Keep sensitive public-repository findings in the configured private reporting path.
- Bind each result to policy/tool versions, exact candidate/build/environment identity, execution identity, and evidence. Reuse unaffected evidence only through explicit validity rules; integrity/security recheck the new candidate. Reconcile external required checks against the exact PR head.
- Add bounded repair/rerun flow driven by actual findings, preserving the same PR and aggregate counters. Accepted oracles cannot be rewritten merely to get green. Record full regression reproductions and concise findings for the implementer.
- Provide the secretless browser/runtime and finite observed-action boundary that 06 can extend. This milestone uses deterministic journeys and scripted negative cases; Jev exploration arrives later.

Exclude System One decision-making, continuous patrols, generic visual taste automation, new production permissions, and protected workflow/policy auto-fixes.

## Checkpoints

1. **Plan and evidence:** gate registry, applicability, result schemas, candidate identity, and invalidation tests.
2. **Independent roles:** isolated reviewer/blackbox/security sessions and secretless executable fixtures.
3. **Readiness and repairs:** publication/check reconciliation, bounded repair, stale/missing result handling, and sensitive reporting.
4. **Hosted demonstration:** prove a failing candidate stays blocked, a validated repair passes, and only then the designated canary becomes review-ready.

## Acceptance

- [ ] **04-A:** Deterministic fixtures require the correct gates for docs-only, ordinary code, public-interface, and sensitive changes. Unsupported mandatory profiles fail closed; no model confidence can produce N/A.
- [ ] **04-B:** Real hosted reviewer, blackbox, and security roles run separately against a designated candidate. Record their session/tool evidence and verify tested code cannot obtain controlling model or publisher credentials.
- [ ] **04-C:** A seeded external-behavior defect is found through a blackbox fixture without implementation-source access. A seeded security defect is detected by the relevant scanner/specialist path. A claimed pass without captured execution is rejected.
- [ ] **04-D:** Missing/crashed/inconclusive gates, wrong commit/artifact identity, and expired evidence prevent readiness. Updating the candidate invalidates affected results and always rechecks integrity/security.
- [ ] **04-E:** A scoped repair updates the same PR, reruns required validation, and preserves counters. Attempted oracle weakening or edits to protected gate policy cannot clear the failure.
- [ ] **04-F:** The complete canary becomes ready only after current sofa gates and repository required checks pass. It remains unmerged for Kevin.
- [ ] **04-G:** Deterministic CLI/library/browser fixtures demonstrate profile-specific testing, bounded target operations, and secretless environments. Performance/accessibility/compatibility hooks capture real configured measurements and report unsupported requirements as blocked.
- [ ] **04-H:** Sensitive findings never enter public logs/issues/artifacts/state as raw content; a missing private destination blocks sensitive publication. Use synthetic secrets/findings to verify this behavior.

## Evidence and handoff

Write `docs/milestones/evidence/04-validation.md`. Include the capability profile, seeded defects, current-candidate gate matrix, live canary references, demonstrated isolation, and the browser/control interfaces available to 06. Document that these checks demonstrate enforced execution/evidence boundaries, not an absence-of-all-defects guarantee.

## Goal prompt

```text
Implement milestone 04 in docs/milestones/04-validation.md, following docs/milestones/README.md and PLAN.md. Build deterministic required-gate planning and independent review, blackbox, and security validation on milestone 01. Prove readiness is tied to current executable evidence, with bounded repairs and isolated credentials. Use subagents for independent gate adapters and adversarial fixtures, and maintain docs/milestones/evidence/04-validation.md. Complete the required local and hosted demonstrations without merging the canary PR, weakening an oracle, or implementing later milestones.
```
