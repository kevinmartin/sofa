# Milestone 05: System One routing and recovery

## Outcome and dependencies

Add optional, measured System One decisions to the existing Go factory: select a permitted execution profile at a stage/turn boundary and classify an unfamiliar failure into an approved diagnosis/repair route. Explicit rules remain first; disabling System One preserves working static execution.

Depends on **03: harness/provider matrix** and **04: mandatory independent gates**. Read [the shared goal contract](README.md), [the main plan](../../PLAN.md), [System One design](../system-one.md), and [lifecycle recovery](../lifecycle.md). Reuse their admission, credentials, Caelis-backed ACP, gate evidence, and cumulative-budget contracts.

## Scope and exclusions

Implement the Go decision interfaces, direct Jev HTTP adapter, optional SemIf subprocess adapter, versioned rubrics, strict result validation, shadow evaluation, profile routing, and failure recovery integration. The Jev adapter requires HTTPS with normal certificate validation, rejects HTTP before attaching credentials, and rejects redirects for credential-bearing requests; cover these boundaries in tests. SemIf uses the pinned upstream `semif-score` JSONL contract; record runtime/model/tokenizer/checksum versions. Implement its adapter and protocol/replay tests even if runner performance keeps the CPU profile experimental.

Route only among prefiltered, trusted model/effort profiles. Explicit choices and a single eligible profile make no decision request. Use advertised, tested ACP configuration between completed turns; otherwise start a configured session at a checkpoint. Reread available reasoning options after model changes. Preserve supported endpoint/auth combinations, required review roles, and publication boundaries.

Exclude per-internal-call proxies, universal live steering, browser navigation, memory ranking, scheduled watches, and new credentials/providers. Those belong to later milestones. Unknown failures may invoke existing diagnosis; classification never authorizes broader repairs, relaxes checks, or publishes protected-policy changes.

## Checkpoints

1. **Decision contract and adapters.** Implement bounded state/questions/candidates, model and policy revisions, distributions, usage, latency, and invalid/unavailable/abstain results. Validate complete answer sets, allowed options, finite probabilities, and bounds. Add deadlines, bounded retry/backoff, process termination, and exact-input versioned caching. Never log keys or unrestricted context.
2. **Routing and recovery.** Connect Go policy decisions to existing profiles and gates. Parse structured failures and known signatures first. For unresolved failures, select among setup/dependency, application, test infrastructure, browser environment, security, and unknown diagnosis roles. Preserve quota/auth handling, exact candidate identity, preconditions, and aggregate retries. Rerun failed and invalidated gates after a repair.
3. **Evaluation and static fallbacks.** Build separate labeled routing and failure fixtures with calibration and frozen held-out partitions. Include ambiguous/no-match cases and malicious instructions in evidence. Compare against static profiles and deterministic failure handling. Record accepted-decision error, abstention/coverage, completed-task outcomes, latency, and known cost. Freeze promotion targets before inspecting held-out results; require zero authorization violations and zero failed gates converted into passes. Automatic decisions default off; missing targets or failed targets keep that family advisory.
4. **Real integration evidence.** Run bounded live Jev calls and a real ACP profile-selection/recovery exercise using an authorized existing credential. Confirm requested settings actually apply and a repair reruns the real gate. Measure SemIf cold/warm latency, memory, setup overhead, and decision quality on the intended hosted runner before advertising CPU support. Protocol fixtures alone do not establish live provider or harness compatibility.

## Observable acceptance

- [ ] **05-A:** Idle, duplicate, known-signature, explicit-profile, and single-candidate paths make zero System One calls.
- [ ] **05-B:** Jev's real typed responses are validated; Noul does not require a nonexistent confidence field.
- [ ] **05-C:** SemIf's implemented adapter handles JSONL, malformed/partial output, cancellation, and runtime failure; unmeasured CPU support is explicitly experimental.
- [ ] **05-D:** Missing keys, timeout, invalid output, or abstention selects the configured static profile or diagnosis fallback without changing credentials or creating a routing LLM call.
- [ ] **05-E:** Unsupported model/effort combinations fail clearly; a tested ACP boundary applies supported changes or resumes through a fresh configured session.
- [ ] **05-F:** Recovery updates the existing task/PR, preserves budgets across reruns, and verifies the repair against current candidate gates.
- [ ] **05-G:** Held-out results and predeclared promotion targets are published separately from calibration results; uncertainty is not presented as a correctness guarantee.
- [ ] **05-H:** Missing required live access remains an incomplete validation item while independent local work continues; failed calibration can legitimately leave automatic decisions disabled.

## Evidence and handoff

During implementation, write `docs/milestones/evidence/05-system-one-routing.md` with commands, fixtures/splits, versioned rubrics, live run references, compatibility results, evaluation targets/outcomes, fallback behavior, and remaining prerequisites. Keep raw sensitive inputs out of the report. Mark observed versus estimated cost and unknown usage. Hand milestone 06 the decision contract, tested profiles, cancellation behavior, and explicit promotion state for each decision family. Do not create this evidence report during planning.

## Paste-ready goal

```text
Implement docs/milestones/05-system-one-routing.md under docs/milestones/README.md. Complete its scoped checkpoints and validation, preserve static fallbacks and authority boundaries, and write the specified evidence report. Keep unavailable required live validation explicitly incomplete while continuing independent work. Stop at this milestone.
```
