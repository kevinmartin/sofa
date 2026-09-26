# System One throughout sofa

Research baseline: September 22, 2026. This is a proposed design, not an implemented integration. Sources below distinguish documented capabilities, creator demonstrations, and sofa's intended behavior. No live model calls were made for this research.

## Role and evidence

System One should participate throughout the factory: choosing execution profiles, detecting stalled work, prioritizing failure recovery, navigating test applications, selecting context, and identifying useful lessons. Its useful boundary is **a bounded judgment over observed state**, not “issue labels only.” Go owns the action space and execution; a generative agent supplies new code, plans, explanations, or test inputs when needed.

Jev's documented interface accepts structured text state and independent typed questions in one request. `Choice` selects among supplied options; `Noul` evaluates a yes/no proposition; `Score` evaluates an ordered rubric. Questions sharing state can run together. A `Choice` supports at most 255 options. This supports composing many small judgments into sophisticated behavior without asking Jev to generate a whole plan. [API](https://docs.typesafe.ai/api), [design principles](https://docs.typesafe.ai/introduction), [composition patterns](https://docs.typesafe.ai/patterns)

The currently documented `jev-1.13.0` is text-only, with a 64k total request budget and a 32k limit for state plus the longest question. Pricing is $0.042 per million input tokens; output tokens are free. Pin the version used for evaluation: `jev-latest` can change behavior. Jev is therefore cheap inference, not zero inference. At that price, 10,000 input tokens cost approximately $0.00042, before other services. [Models](https://docs.typesafe.ai/models)

Evidence worth borrowing:

| Status | Source | What it establishes |
| --- | --- | --- |
| Documented API | TypeSafe | Typed parallel judgments are supported; application code composes results. |
| Creator implementation | [Distill](https://github.com/samuelfaj/distill/blob/main/docs/jev-routing.md) | A coding harness uses Jev for model and effort selection, utility routing, quality checks, and context retention; supplied candidates and result validators constrain choices. |
| Creator implementation | [Jev Codex Router](https://github.com/0xNatoshi/jev-codex-router) | A Responses proxy selects model and effort on internal calls, including tool continuations. Its historical savings figure is a simulation of an older policy, not measured equal-quality savings. |
| Creator demonstration | [Jev Ultrafast](https://github.com/browser-use/jev-ultrafast) | Jev chooses browser operations and observed targets; a small text model supplies field values. This is a practical basis for adaptive test navigation. |
| Architectural experiment | [Foreman](https://github.com/thruwire/foreman) | A concurrent supervisor assesses progress, drift, verification needs, and escalation while a coding worker runs. Its authors explicitly describe supervision accuracy as unproven. |
| Creator evaluation | [Hippo's Jev reranker](https://github.com/kitfunso/hippo-memory/blob/master/docs/evals/2026-09-19-jev-reranker.md) | Better ranking on two corpora did not establish better answers than its local cross-encoder. Context reduction remains a useful hypothesis to test. |

TypeSafe's headline speed and cost comparisons use its own workflow evaluations, with large-model reference probabilities rather than externally verified software outcomes. The launch article describes geographic, workflow-selection, and comparator limitations. Neither those results nor a browser demo establish sofa's end-to-end reliability or savings. [Evaluation methodology and limitations](https://typesafe.ai/blog/introducing-system-one-models-and-jev)

## Proposed decision surface

These are sofa design proposals. Each optional semantic decision runs only after cheaper structured checks fail to settle the question, and only when its answer can change useful behavior.

| Factory area | Deterministic first | System One contribution | Executor or final oracle |
| --- | --- | --- | --- |
| Discovery | Required fields, duplicate identifiers, authorized intake | Rank research questions; detect missing acceptance criteria; select a specialist | Research agent drafts a sourced specification; owner approval admits it to Backlog |
| Planning | Available recipes, dependencies, required roles | Assess ambiguity and effort; rank permitted approaches | Planner writes novel design; policy fixes required gates |
| Model routing | Endpoint, auth, capabilities, budget, explicit overrides | Select sufficient model/effort from eligible profiles | ACP adapter applies supported settings |
| Work supervision | Exit status, time limit, repeated command/failure fingerprints | Assess semantic progress, drift, or need for another specialist | Controller continues, checkpoints, or escalates |
| Workflow recovery | Parse structured failures and known signatures | Classify unfamiliar evidence into a known cause taxonomy | Approved recipe or fixer agent, followed by rerun |
| Blackbox E2E | Stable recorded journeys and assertions | Select next operation/observed target; recognize obstacles | Browser executor and independent assertions |
| Adversarial exploration | Seeded invalid inputs, scanners, access-control matrix | Prioritize unexplored states and attack scenarios | Disposable test environment; reproducible failing test |
| Security review | Secret scanning, dependency and static-analysis results | Rank findings and select deeper inspection targets | Security tools and specialist review; no semantic pass override |
| Taste and maintainability | Format, lint, complexity, duplication | Evaluate explicit semantic preferences; rank actionable concerns | Bounded maintenance PR with existing checks preserved |
| Memory retrieval | Scope/version filters, exact lookup, lexical retrieval | Rank candidate episodes, facts, and skills for current work | Token-bounded context builder with source references |
| Dreaming | Aggregate repeated failures, cost and outcome deltas | Detect reusable patterns, contradictions, and recipe candidates | Agent proposes reviewed memory, skill, or Go changes |

Mandatory E2E, security, or review roles are selected by trusted configuration and deterministic risk rules. System One may add investigation or prioritize it; it cannot remove a required role because it predicts that the change is easy.

## Controllers and execution boundaries

### Model and effort selection

Maintain a trusted catalog of execution profiles: harness, provider endpoint, credential source, model, supported reasoning settings, tool/vision/context capabilities, and budget constraints. Filter it before inference. Explicit selections and a single eligible profile bypass Jev. Otherwise provide task phase, unresolved complexity, recent failure evidence, and compact candidate descriptions.

Start with routing at stage boundaries and between completed ACP turns. ACP v1 optionally exposes `configOptions`, including model and thought-level selectors, through `session/set_config_option`. The specification allows changing options during generation, but this does not supply a portable interception callback before every underlying model request. Read the returned option state again after model selection because valid reasoning choices may change. [ACP session configuration](https://agentclientprotocol.com/protocol/v1/session-config-options)

Use advertised, integration-tested settings; otherwise start a fresh configured session at a checkpoint. Persist a handoff with the task, artifact references, unresolved findings, and workspace revision. Never imply that hidden reasoning or harness-private session state transfers between agents. Unsupported combinations fail diagnostics instead of silently using different billing or authentication.

A later opt-in, harness-specific proxy can implement per-internal-call routing. It must preserve the native streaming/tool protocol, handle continuation and compaction state, avoid recursive routes, and measure cache disruption. Jev Codex Router's [routing policy](https://github.com/0xNatoshi/jev-codex-router/blob/main/server/routing_policy.py) illustrates separate tier, effort, and bounded route-duration questions; its policy is an example, not sofa's calibrated policy. Keep a selected route through a stable stage unless new evidence justifies changing it. Optimize completed-work cost, including correction and context reprocessing, rather than maximizing cheap-model calls.

Authentication stays isolated per profile. A model choice cannot authorize a new endpoint, forward a subscription token to a router, enable paid fallback, or expand data-sharing permissions.

### Progress and failure recovery

Observe ACP events, changed paths, artifact digests, test results, elapsed time, and a short output tail. Debounce assessment on meaningful new evidence; do not continuously rescore identical state. Deterministic limits remain active if Jev fails. Initially record progress judgments without stopping useful work; later permit bounded checkpoint/escalation decisions after evaluation. Standard ACP cancellation is usable; live steering requires a separately verified harness capability.

Failure recovery follows:

`observe → parse → known signature/recipe → semantic cause classification if unresolved → validate remediation preconditions → repair → rerun exact failed validation → reconcile`

Use causes such as transient infrastructure, provider quota, authentication, environment/setup, dependency resolution, application regression, flaky-test candidate, workflow configuration, and unknown. An exact rate-limit response needs no Jev. A proposed flaky-test classification cannot turn a failed check green or justify unlimited retries.

Each remediation defines applicable causes, evidence requirements, permitted scope, retry limit, and completion check. Repeated failure fingerprints exhaust a shared persisted budget. Authentication errors stop for credential repair; quota cases defer; unknown causes go to diagnosis. Workflow-definition and protected-policy fixes become patch/specification proposals for owner handling; ordinary repair grants cannot publish them or make a failing run waive its own checks. [Jev Logtriage](https://github.com/jyatesdotdev/jev-logtriage) demonstrates typed log triage with code-owned action mapping, but deliberately executes no remediation itself.

Configured watches may open bounded repair PRs under trusted standing policy; every resulting PR still requires human merge. System One routes work inside that preauthorized scope. It does not infer authorization from logs or create new permission through a diagnosis.

### Adaptive browser and adversarial testing

Keep deterministic E2E journeys as regression tests. Add an optional exploratory lane in disposable previews using synthetic accounts and bounded test data. A planner supplies a mission and independent assertions; Go exposes only allowed operations against the current observed DOM/accessibility nodes. Batch the operation question with speculative target questions, then execute only the target belonging to the chosen operation. Revalidate the observation, target, origin, and action budget immediately before input.

Use fixtures or deterministic generators for ordinary and malformed inputs; ask a generative model only for novel text. Jev selects indices, never selectors, shell commands, or executable JavaScript. Its current text-only input cannot perform screenshot-based visual assessment; use a separate vision-capable profile for that purpose.

The [Ultrafast loop](https://github.com/browser-use/jev-ultrafast/blob/main/jev_ultrafast/agent.py) consumes a decision once, records execution before observing again, and bounds actions and calls. Its [performance report](https://github.com/browser-use/jev-ultrafast/blob/main/docs/performance.md) reports three matched Google Flights pairs, not a broad benchmark, and explicitly warns that `DONE` is not independent evidence of success. Sofa must verify postconditions separately. Preserve action traces, screenshots, network/console failures, and reproduction inputs; confirmed discoveries become deterministic regression tests. A stalled, incomplete, or unsupported flow is inconclusive, never a passing gate.

### Memory and dreaming

Retrieve a deterministic shortlist from repository-scoped semantic facts, episodic records, and approved procedural skills. Jev can rank relevance, identify contradictory candidates, or assess whether a lesson generalizes. Preserve stable ordering for probability ties; missing or invalid scores fall back to lexical results. No-match queries must be tested explicitly: ranking first does not establish relevance.

Maintain provenance and distinguish observed outcomes from agent claims. Current code, configuration, and live state outrank memories. Pin essential instructions outside the optional retrieval budget; learned context cannot change authorization. Do not let Jev destructively remove historical evidence during compaction: retain recoverable originals under the repository's retention policy.

Dreaming runs after deterministic aggregation finds new material. It evaluates recurring mistakes, redundant inference, overlooked procedures, and stale guidance, then proposes sourced semantic updates, skill changes, or deterministic recipes through normal review. Metrics and experimental outcomes accompany a proposal; a persuasive lesson is not proof of improvement. [Beacon](https://github.com/Asymptote-Labs/agent-beacon) provides a useful cross-harness capture → evaluation → reviewed memory pattern. Sofa adopts the pattern without requiring another memory service.

## Go contracts, evaluation, and rollout

Keep the existing Go `DecisionProvider` boundary and implement a small direct HTTP adapter. The official API already provides the needed wire contract; no additional agent harness is required to ask Jev questions.

- `DecisionRequest`: decision kind, versioned question set, bounded state/evidence references, allowed candidates, policy revision, deadline and input budget.
- `DecisionResult`: answered model revision, typed values/distributions, optional provider confidence, usage, latency, and explicit invalid/unavailable/abstain outcomes.
- `PolicyDecision`: selected permitted route/action, observation digest, required preconditions, expiry, and fallback. This is produced by Go, not accepted verbatim from the model.
- Action implementations consume typed identifiers and trusted configuration. Raw model strings never become commands. Cache keys include exact state, questions, candidates, model, and policy versions; live authorization and freshness are checked again before execution.

Validate complete answer sets, choice membership, finite probabilities, and response bounds. Choice/Score confidence is a statistic derived from a distribution, not the measured probability that an action succeeds; Noul has no separate confidence field. [Confidence contract](https://docs.typesafe.ai/confidence)

Evaluate each decision family separately on labeled sofa cases, including ambiguity, missing candidates, prompt injection, stale evidence, and provider failures. Compare deterministic-only, static-agent, and Jev-assisted paths. Use held-out cases and completed-task outcomes: false escalation, missed defects, false completion, repair success, total latency, tokens, observed cost, and unknown usage. Do not count missing usage as zero or treat replay cost estimates as observed savings. The independent [Janus study](https://github.com/FirasSX914/Janus/blob/main/RESEARCH.md) found opposite cost/quality outcomes for the same cascade on two datasets; thresholds must be measured locally.

Roll out in three increments:

1. **Foundation:** versioned provider contract, offline fixtures, telemetry, failure taxonomy, stage-level routing and memory-ranking shadow evaluations. Static execution remains available without Jev.
2. **Useful first release:** opt-in evaluated profile selection and cause routing; bounded exploratory browser lane; semantic preference reports; evidence-triggered dreaming proposals. Required gates remain deterministic and independent.
3. **Measured expansion:** per-call proxy integrations, active semantic supervision, richer adversarial navigation, and context reduction where held-out outcomes justify them. Reevaluate when models, rubrics, action sets, or workloads change.

The goal is a factory that progressively replaces repeated inference with tested procedures, while using inexpensive semantic decisions wherever explicit rules alone are insufficient.
