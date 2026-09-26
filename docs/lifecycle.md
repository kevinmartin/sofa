# Lifecycle, board, and validation design

This is the proposed full lifecycle for sofa. It extends the delivery loop in [PLAN.md](../PLAN.md); it does not create workflows or configure the user's GitHub Project yet.

## Board and approval boundaries

Use the built-in GitHub Project Status field for these canonical states, with configurable display names:

**Inbox → Discovery → Spec Review → Backlog → Ready → Building → Verification → Review → Release → Done**

Create two focused views over the same items: a **Product** view covering Inbox through Backlog, and a **Delivery** view covering Ready through Done. A complete view remains available for auditing. A column represents a meaningful handoff; individual agent/tool steps remain execution phases, not additional columns.

| State | Purpose and exit condition | Authority |
| --- | --- | --- |
| Inbox | Capture ideas, public reports, and deduplicated watch findings without spending model budget automatically | Anyone may submit input; submission grants no execution permission |
| Discovery | Research the problem, reproduce evidence, investigate constraints, and draft a specification | Owner admission or an explicit discovery-watch policy |
| Spec Review | Present the proposed scope, acceptance criteria, risks, unknowns, and alternatives | Kevin approves the exact specification revision before Backlog |
| Backlog | Store approved, sufficiently understood work; record dependencies and priority | Agents may suggest ordering; they cannot silently authorize implementation |
| Ready | Authorize delivery of the approved scope when dependencies and prerequisites are satisfied | Kevin, or a narrowly scoped standing watch policy through its separate admission path |
| Building | Plan/decompose as needed, implement, and run local checks | Admitted worker operating within the task budget |
| Verification | Run the configured independent validation DAG against the exact candidate revision | Deterministic controller dispatches required tools and specialist roles |
| Review | Present the PR and evidence for human review; process authorized feedback | Kevin approves merge; bounded repairs return to Building/Verification |
| Release | Observe post-merge build/package/deployment and required smoke checks | Existing repository release policy; sofa does not acquire new production deployment authority |
| Done | The repository's declared completion conditions have passed | Deterministic reconciliation based on live evidence |

Use separate fields for `blocked_reason`, `next_action`, `source`, `risk`, and `delivery_status`, rather than moving every blocked item into one column that hides its lifecycle position. The operational ledger may still record Blocked, Deferred, Cancelled, or Superseded outcomes. Done items retain their evidence and can link to a new regression item.

Kevin selected **specification approval before Backlog**. Discovery must not approve its own output. Present a versioned specification in the issue; Kevin's move from Spec Review to Backlog is the approval gesture. Sofa computes the canonical digest internally and snapshots the observed revision, rejecting edits newer than the Backlog field revision. The Project's write ACL, not an unavailable status-event actor, is the authorization boundary; keep Project write access restricted and do not let factory automations set Backlog or Ready. Persist bounded specification snapshots under `specs/<work-item>/<digest>.md` on `sofa-state`, separately from the compact ledger, so approval does not depend on expiring artifacts. Apply the repository's visibility/content policy to these snapshots.

Approval binds the canonical specification fields. A changed goal, acceptance criterion, or constraint returns to Spec Review; deterministic normalization can ignore formatting-only differences. Additional evidence is stored separately and invalidates approval if it changes those fields. Sofa binds the revision observed after the board move and rejects later edits; it cannot reconstruct the exact content Kevin saw at click time. GitHub may omit the mover from its public API, so sofa must not claim to have verified Kevin personally made the move.

Kevin also selected **bounded automatic fix PRs from configured watches**. A watch's standing grant is an alternative to manually moving an item to Ready; it is not a fabricated human board event. A qualifying small repair may enter Building directly, with a visible source/policy link. Findings requiring discovery follow the normal specification approval path. No watch may auto-merge.

## Intake, discovery, and planning loops

Normalize every trigger into a work item plus admission proof: source object/revision, repository ID, originating loop, human actor or policy revision, allowed change class, expiry, budget, and parent task if any. The controller validates the proof before fetching credentials or starting a model. Repeated reports update a stable finding instead of opening duplicate work.

Discovery produces a versioned specification artifact linked from the issue. Its minimum contents are:

- Problem, intended user, and evidence that it exists; a reproducer where applicable.
- Goal, non-goals, relevant constraints, dependencies, and affected interfaces/components.
- Observable acceptance examples, including negative/error behavior; a proposed validation profile.
- Known security, data-handling, migration, and operational risks, with unanswered questions marked explicitly.
- Small delivery slices where needed, each with a usable outcome and dependencies.

The discovery agent may research documentation, inspect the repository, and run bounded non-production reproductions. It does not publish implementation changes or silently expand the task. Go extracts facts from manifests, ownership, existing tests, and prior evidence first. Jev can organize ambiguity, candidate duplicates, or complexity; generative research is reserved for unresolved questions.

Backlog grooming runs against changed/stale items, not as a recurring rewrite of every specification. It detects missing dependencies, changed source assumptions, superseded requirements, and conflicting work. It proposes corrections or re-review. Priority remains an explicit owner field unless a standing policy permits a defined ordering rule.

Start with two active discovery items and one active code-writing delivery item per repository. Verification jobs for that item may run in parallel within a configured cap. Background improvement work yields to admitted delivery and broken gates. Splitting one task must not reset its aggregate spending/repair counters.

## Deterministic validation with specialized agents

At admission, Go creates a **gate plan** from the trusted repository capability profile, approved specification, changed surfaces, and standing policy. Recompute applicability when the patch changes. A model may recommend additional gates; it cannot remove a required one. Required specialist roles are dispatched explicitly instead of relying on an implementer to remember to request them.

| Gate | Execution and evidence | Default applicability |
| --- | --- | --- |
| Integrity | Scope/path checks, patch and artifact provenance, secret scan, approved policy and dependency verification | Every publishable change |
| Build and repository checks | Trusted commands; process exit status and machine-readable build/lint/unit/integration results | Repository capability profile; docs-only exceptions must be explicit |
| Independent code review | Fresh reviewer session, diff/spec/context, actionable findings and evidence references | Novel code; approved recipes may use their tested deterministic review policy |
| Blackbox acceptance | Tester gets approved behavior plus a built artifact/preview and runs public-interface scenarios | UI, API, CLI, and consumer-facing library changes using the relevant profile |
| Security validation | Deterministic dependency/static/security checks; a separate security agent for threat-oriented assessment | Scanners for applicable code; specialist required for authentication, authorization, external input, secrets, dependencies, and configured sensitive surfaces |
| Adversarial exploration | Bounded browser/API/CLI exploration with explicit invariants, synthetic data, and reproducible failures | Changed exposed surfaces plus configured sampling; deterministic regression scenarios remain required |
| Performance/accessibility/compatibility | Repository-defined measurements, accessibility checks, supported-platform/API contract tests | Required by capability profile or acceptance criteria |
| Release verification | Post-merge build/package/deployment status and smoke checks bound to the released commit | Configured release path; otherwise merge plus default-branch acceptance completes delivery |

Gate outcomes are **Passed, Failed, Blocked, or Not Applicable**. Not Applicable requires a deterministic, policy-approved reason. Missing profiles, incompatible agents, crashed testers, missing artifacts, inconclusive required evidence, and unavailable environments are Blocked—not Passed. Required security or acceptance findings cannot be waived by Jev confidence or an LLM's summary.

Each gate result records its ID/policy version, source commit, candidate tree/commit digest, built artifact digest, environment identity, tool/role versions, execution identity, outcome, and evidence references. Readiness requires compatible results for the same candidate. A changed candidate invalidates affected gates; security/integrity gates always recheck the new artifact. Repository required checks remain authoritative alongside sofa's own readiness check.

An agent's finding is useful evidence, but a claimed test pass is not a replacement for captured test execution. Go validates actual tool results and configured acceptance oracles. The deterministic guarantee is that applicable roles ran and supplied valid evidence; it does not guarantee the absence of undiscovered defects.

### Blackbox testing and adversarial browser use

Give the blackbox tester a separate workspace/session containing the approved specification, public API/CLI descriptions, test fixtures, and an isolated preview or packaged binary. Withhold the implementation agent's reasoning and source tree by default so the tester assesses external behavior independently. A library uses a consumer fixture; a CLI uses subprocess I/O; a web app uses a browser. A browser is not mandatory for every repository.

Run against the exact candidate with synthetic accounts/data and a disposable environment. Scope target origins and operations explicitly. Tests do not receive publication, production, billing, or unrelated provider credentials. Existing test suites run normally; independent testing supplements them.

For exploratory browser testing, Go observes accessibility/DOM state and generates a bounded action set with stable element IDs. Jev can choose a goal-directed action or identify apparent progress; the executor validates the action against the current page generation, target allowlist, and action budget. A separate visual-capable model handles visual judgments where necessary. Use explicit oracles such as role boundaries, persisted state, API errors, and invariant checks; “the page looks fine” alone is insufficient. The detailed decision design is in [System One](system-one.md).

Record action traces and turn reproducible failures into deterministic regression tests. Distinguish an observed defect from an untested suspicion. A failed invariant cannot be cleared by a new model judgment. The implementation agent receives the reproducer and fixes the product; it cannot rewrite an accepted oracle simply to turn the gate green. Legitimate specification changes require the original approval path.

### Failure → diagnosis → repair → validation

1. Go parses machine results and checks exact revision, cancellation, resource loss, quota/auth status, and known failure fingerprints.
2. An approved deterministic recovery recipe handles known transient cases within the existing budget.
3. For unfamiliar evidence, Jev ranks predefined causes and selects an allowed diagnostic/fixer role: setup/dependencies, product bug, test infrastructure, browser environment, security, or unknown. Its answer is a hypothesis to investigate.
4. The selected specialist gathers evidence, reproduces the failure, and either proposes a repair or reports a blocker. A suspected flaky test still fails until the configured flake policy and evidence justify the next action.
5. A validated patch updates the same task/PR. Rerun the failed gate and gates invalidated by the change. Preserve attempt counts across Actions reruns and child tasks.
6. Exhaustion, recurring fingerprints without progress, ambiguous authority, credential repair, or protected workflow changes stop automatic repair and produce one actionable escalation.

Workflow/action definitions, credentials, release permissions, gate thresholds, admission rules, and provider/data-routing policy remain protected. A fixer prepares a patch/specification proposal for owner handling; ordinary fix and dreaming grants cannot publish those changes or rerun with broader credentials. A dedicated governance publication path is outside the initial release. Never retry merely to obtain a random green result.

## Watch authority, release, and learning

Each configured watch has an explicit standing grant: detector, allowed repositories/paths/change classes, evidence prerequisites, profiles, maximum attempts/PRs/resource use, and expiry or disabled state. Commit that grant to the trusted default branch. The controller records the exact grant revision with each autonomous task and checks it again before publication. Removing or narrowing a grant affects pending work at the next enforcement point.

For example, a reproducible bug in an allowlisted module may authorize a small fix PR; an architectural redesign discovered during diagnosis becomes a Discovery proposal. Never use a probabilistic complexity score alone to enlarge the grant. Shared credentials do not imply shared authority between loops.

Post-merge verification observes existing deployment/release workflows. For a deployable application, Done requires the configured release and smoke evidence; for a library or toolkit it can require packaging/publishing or simply default-branch checks according to policy. Failed release verification becomes a linked incident/fix task, without retrospectively pretending the merge never happened. Production rollback remains under the repository's explicit release policy; the factory has no implicit rollback/deploy permission.

All phases emit compact observed outcomes into episodic memory. Weekly dreaming can propose corrections, reusable skills, deterministic recipes, missing tests, and better routing. Curated guidance and executable improvements activate after reviewed merge and evaluation. See [memory](memory.md) and the [watch catalogue/runtime design](runtime-and-watches.md).

## Acceptance and rollout

Ship the lifecycle in vertical slices: first one discovery/spec-approval flow and one delivery flow, then the mandatory specialist gate framework, then scoped watches and memory/dreaming. Keep all code/harness requirements from the main plan; these are implementation milestones rather than reasons to omit lifecycle support from the release definition.

Acceptance fixtures must prove:

- Discovery cannot move an unapproved or materially edited specification to Backlog.
- Human Ready and standing-watch admissions remain distinguishable, idempotent, and revocable.
- A watch within its grant can open one bounded fix PR; scope expansion, missing proof, or depleted budget stops it.
- Every applicable required specialist runs against the current candidate; crashes and unsupported profiles block readiness.
- Blackbox testers cannot inspect the source by default or target production accidentally; exploratory findings can be replayed.
- Implementers cannot edit accepted oracles, lower security thresholds, or use stale evidence to clear a gate.
- Failed post-merge verification cannot become Done; observation does not grant deployment authority.
- Repeated failures, late events, interrupted workers, and duplicate reports neither create duplicate PRs nor reset budgets.
