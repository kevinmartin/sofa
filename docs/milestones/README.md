# sofa implementation milestones

These specifications turn [the architecture plan](../../PLAN.md) into bounded implementation goals. They are work instructions, not evidence that any feature has been built. Start with milestone 01. Use one goal at a time by default; later goals consume the implementation and evidence left by earlier goals.

## How to use these files

Open a task in the sofa project, read the milestone, and paste its final prompt as the `/goal` objective. Each prompt points back to this shared contract and its specification, so a new task does not need the original conversation. Do not paste the whole roadmap into one goal. No goal, automation, repository, or external service is created by these documents.

The four broad development stages have been divided into smaller goals, followed by release qualification. The first goal deliberately crosses the stack with one narrow example; it proves the critical integration before the factory grows.

| Goal | Observable outcome | Prerequisites |
| --- | --- | --- |
| [01 — Safe hosted delivery slice](01-delivery-slice.md) | One approved issue produces one validated draft PR using Copilot; interruption and duplicate delivery are recoverable | None |
| [01.1 — Deterministic E2E regression harness](01.1-e2e-regression-harness.md) | Fast fault scenarios and a hosted fake-ACP canary protect the delivery loop without recurring model requests | 01 |
| [02 — Lifecycle and Discovery](02-lifecycle.md) | Discovery, specification approval, board handoffs, review feedback, and release observation work | 01 |
| [03 — Harnesses and model endpoints](03-harnesses.md) | Copilot, Codex, and Claude work through tested, isolated profiles and compatible routers | 01 |
| [04 — Independent validation](04-validation.md) | Required reviewer, blackbox, security, and repository gates control PR readiness | 01 |
| [05 — System One routing and recovery](05-system-one-routing.md) | Evaluated typed decisions select permitted profiles and investigations, with deterministic fallbacks | 03, 04 |
| [06 — Adaptive testing and supervision](06-adaptive-testing.md) | Bounded browser exploration and progress assessment operate against independent oracles | 04, 05 |
| [07 — Useful memory](07-memory.md) | Episodes, semantic knowledge, and skills can be stored and retrieved with scope and freshness controls | 02, 04, 05 |
| [08 — Bounded watches](08-watches.md) | Configured maintenance loops can produce useful findings and bounded fix PRs | 02, 04, 05, 07 |
| [09 — Dreaming and improvement](09-dreaming.md) | New outcomes produce evaluated, reviewable improvement proposals without self-triggering loops | 07, 08 |
| [10 — Release readiness](10-release-readiness.md) | Public/private canaries and recovery/security checks qualify a reproducible release candidate | 01, 01.1, 02–09 |

Recommended execution order is numerical: run 01.1 after 01 to protect the proven delivery loop before extending it. Goals 02–04 retain independent feature boundaries and can be implemented in parallel after their shared contracts are frozen. Assign separate branches/worktrees when available and give one coordinator responsibility for integration. Within any goal, delegate bounded adapters, fixtures, or reviews while the coordinator works on another useful part. Avoid simultaneous uncoordinated edits to configuration, state schemas, and publication logic.

## Shared goal contract

### Sources and scope

Read the selected milestone, [PLAN.md](../../PLAN.md), and the relevant linked design documents. Preserve the user's settled product choices. The milestone narrows the work due now; exclusions defer features without deleting their roadmap requirements. If code already exists, inspect it and prior evidence before changing it. Reuse completed work instead of restarting.

Implement only the selected milestone and small prerequisites necessary to complete it. Do not silently begin the next milestone. Prefer concrete interfaces needed by current consumers; avoid scaffolding every future subsystem. Record material design changes and their evidence in the handoff. Routine implementation choices are the implementing agent's responsibility; changes to product authority, privacy, billing, or required release scope need an explicit owner decision.

### Invariants carried into every milestone

- All custom executable implementation, helpers, and test drivers in sofa are Go. YAML/Markdown/configuration, fixtures, and third-party harness/model/browser runtimes are allowed. Use the selected Caelis SDK behind a private adapter; verify and pin compatible versions when implementing.
- Go owns admission, lifecycle, budgets, required gates, and external publication. Idle/repeated/unauthorized input and applicable exact recipes use zero model inference. Missing System One falls back by decision type, not automatically to another model call.
- Each consumer owns its execution, state, and explicitly passed credentials. No central controller, required database, or cross-repository write capability. Keep work-source/runtime boundaries sufficient for later adapters without implementing Linear or another executor now.
- Discovery cannot approve its specification. Kevin approves the displayed specification digest before Backlog; Ready authorizes normal delivery. Explicit standing watch grants provide a separate admission path after 08. Kevin retains merge authority.
- Workers, product tests, and trusted publication have separate permissions. Tested code receives no factory/provider secrets; a controlling specialist receives only its configured model credential. Validate provenance, paths, current candidate, live authority, and ownership before publication. Repository instructions and retrieved memory cannot change these boundaries.
- Persist identities, ownership, checkpoints, and aggregate budgets. No force-push over human work, duplicate PR creation after retry, optimistic completion from missing evidence, or budget reset through child work.
- Only configured endpoints/auth sources may be used. No automatic paid fallback, native subscription credential forwarding to a router, production deployment, or automatic merge.
- Ordinary fix/dreaming lanes cannot publish protected workflow, permission, gate-threshold, or provider-routing changes. Sofa development itself may edit its workflows as reviewed implementation work; that does not grant deployed sofa agents the same authority.

### External prerequisites and live work

Each milestone names its live prerequisites. Discover existing repository configuration and authorized credentials first. Ask for missing repository IDs, credential references, test targets, or limits early when they are actually needed, while continuing independent local implementation and checks. Never ask for secret values in chat or print them in evidence.

A future implementation goal permits building and validating its scoped feature. Unless the owner or milestone explicitly designates one, it does not name a GitHub test target, authorize creating arbitrary repositories or changing production settings, supply a spending budget, or authorize a stable release. Use owner-designated disposable resources and configured finite budgets. Prepare concrete setup artifacts before requesting any missing external action. Do not re-request permission already supplied in that implementation task.

Mocks and replay fixtures prove local behavior; they do not prove hosted Actions or real harness interoperability. If a required live check is unavailable, record local results, the exact remaining check and prerequisite, and continue other useful work. Keep the milestone incomplete rather than relabeling the check optional. Respect the goal system's own completion/blocking rules; the progress labels below are document status, not tool commands.

### Checks, evidence, and completion

Use observable acceptance scenarios with positive, failure, and interruption cases that protect real behavior. Run checks appropriate to changed components and relevant regressions. Do not repeat unrelated expensive/live suites merely because another milestone started. Live measurements include versions, environment, sample size, budgets, and limitations; unknown cost remains unknown.

During implementation, maintain `docs/milestones/evidence/<milestone-file-name>.md`. The completed 01 report is included; reports for later milestones are intentionally absent from the planning package. Each report contains:

1. Status: **Not started**, **In progress**, **Local checks passed; live checks pending**, or **Complete**.
2. Implemented behavior and material decisions, with source/config/schema revisions.
3. Each acceptance ID, its observed result, and a command/test or redacted artifact/run/PR reference. Mark unavailable evidence explicitly.
4. Live resource/profile references and measured usage; never secret values or sensitive raw findings.
5. Known limitations, remaining prerequisites, and how to reproduce/recover the demonstration.
6. A compact handoff: interfaces/configuration added, migrations if any, and what the next goal can rely on.

Completion requires all mandatory acceptance checks and deliverables for that milestone, including specified live checks. An intentionally advisory semantic policy can be a valid implemented outcome when the spec allows it; an untested provider cannot be called supported. Do not demand proof that a classifier always wins or a system has no possible vulnerabilities. Report what the evidence actually establishes.

Leave changes concrete and reviewable. Follow the implementation task's repository/PR instructions; commit or PR metadata belongs in the report when available. PR merge and stable release remain owner-controlled. At the end, report the outcome, evidence, and remaining limitations, then stop before the next goal.

## Scope after these goals

Per-internal-model-call routing proxies, persistent Codex subscription refresh, Linear intake, alternative runtimes, shared private cross-repository memory, a governance publication lane, and an external heartbeat service remain later work. The initial release includes the three ACP harnesses, full lifecycle, required validation, evaluated System One capabilities, memory, bounded watches, and dreaming described here. Goal 01 alone is a prototype, not that release.
