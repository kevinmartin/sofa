# Memory and learning

Planning baseline: September 22, 2026. This document specifies proposed behavior, not an implemented memory service. See [the main plan](../PLAN.md) for execution, publication, and credential boundaries.

`sofa` should accumulate useful experience without turning every historical assertion into an instruction. Retaining an observation, retrieving an agent's hypothesis, and promoting a procedure are different operations with different trust requirements. Go owns storage, retrieval boundaries, permissions, and budgets; models assist where interpretation is useful.

## 1. What belongs in each store

| Store | Contents | Storage and authority |
| --- | --- | --- |
| Operational ledger | Admission, ownership, phase, retry counters, dispatch cursors, exact commit heads | Existing `sofa-state` branch; trusted Go reconciler writes authoritative execution state |
| Episodic memory | What happened during an attempt: context, actions, outcomes, failed approaches, evidence references, duration and usage | Separate `episodes/` namespace on `sofa-state`; automatic validated append |
| Semantic memory | Architecture, domain vocabulary, decisions, constraints, validated gotchas | `.sofa/memory/semantic/` on the caller's default branch; curated through PRs |
| Procedural memory | Reusable skills, applicability, steps, validations, failure handling | `.sofa/skills/` on the caller's default branch; shared skills accompany sofa releases |
| Evidence artifacts | Redacted observable events, logs, screenshots, test traces, patches, agent explanations | Actions artifacts with bounded retention; evidence rather than execution authority |

The ledger answers “may this worker publish?” Episodic memory answers “what happened last time?” Semantic memory answers “what do we currently know?” Procedural memory answers “how should we perform this kind of work?” Keeping them separate prevents an old incident summary from overriding current authorization.

An episode can establish that a check failed, a repair was attempted, and another check passed. Its proposed root cause can remain uncertain. Store machine observations automatically; agents may also retain bounded, source-linked hypotheses automatically under the repository's content policy. Mark hypotheses explicitly and retrieve them as leads to verify. Human review is required to promote guidance, not to retain every observation.

Prefer references to authoritative code, configuration, and ADRs over duplicate prose. Go should derive package managers, configured checks, and current tool versions directly when possible. Computing a fact cheaply is preferable to repeatedly asking a model to remember it.

## 2. Records, skills, and ingestion

Use versioned JSON records with a shared envelope:

```text
schema_version, id, kind, repository_id, visibility
domain, path_anchors, observed_at, source_revision
origin { observer, harness, model, toolkit_version }
evidence[] { repository_id, commit, path, blob_hash, run_id, check_id, url }
status, supersedes[], derived_from[], expires_at
payload
```

Required fields depend on kind; absent provider usage remains unknown. Stable IDs and evidence references are mandatory. Record origin separately from verification: an agent writing valid JSON does not make its claim verified.

- **Episodes:** payload contains attempt/stage IDs, exact base/head, selected profile/recipe, observed result enums, check references, retries, duration, available usage, and normalized failure fingerprints. Optional hypotheses include a concise claim, supporting evidence, and unresolved alternatives. Corrections append a linked record rather than rewriting history.
- **Semantic records:** payload contains one concise claim, applicability, authoritative references, last verification, and any caveat. Status distinguishes candidate, active, disputed, superseded, and expired. A reviewed architectural decision can remain active until explicitly superseded; an observation about a remote service needs a freshness deadline.
- **Skills:** a directory contains `SKILL.md`, optional references, fixtures, and a sofa metadata file recording version, applicable roles/components, preconditions, expected outputs, validation, evidence, and compatible tooling. Scripts or executable helpers are reviewed code subject to the ordinary sandbox policy. New custom implementation remains Go.

The Agent Skills format provides portable metadata and progressive loading of instructions and resources. Sofa adds deterministic validation around it; skill metadata cannot grant permissions or override failed checks. [Agent Skills specification](https://agentskills.io/specification)

Ingestion occurs after meaningful stage boundaries. A trusted finalizer validates run identity, ownership, artifact provenance, schema, record size, allowed paths, and content policy before appending observations. It can record an interrupted or failed stage without a successful agent response. Reconciliation appends linked outcome/correction events for later owner feedback, merges, reverts, and release observations, so learning can revisit an already-consumed episode. Proposed explanations retain their untrusted status even when the surrounding record was written by trusted code.

Keep raw transcripts opt-in. Record observable actions, decisions, outputs, and concise explanations; hidden reasoning is neither required nor a durable record format. Redact sensitive outputs before storage and apply the same policy to screenshots, filenames, links, and summaries. Secret scanning reduces accidental disclosure but cannot prove arbitrary content harmless.

## 3. Retrieval within a budget

Materialize a read-only selected memory snapshot for each stage; do not preload the full corpus or auto-load every skill. Trusted execution policy is loaded separately and never competes with memories in search ranking.

1. Filter by repository, visibility, allowed model endpoint, role, applicability, status, and expiry before searching.
2. Match exact issue/run IDs, components, file anchors, approved skill triggers, and failure fingerprints.
3. Rank remaining candidates deterministically using lexical/BM25 search, evidence relevance, and recency. Return provenance and selection reasons.
4. If ambiguity remains, Jev may rank an already permitted shortlist. It cannot expand the visible corpus or activate an unapproved procedure.
5. Inject concise records; fetch full evidence only when a concrete question requires it. No relevant result is a valid outcome.

Default sofa-managed recall budget: **4,000 tokens per agent stage**, initially allocating 2,000 to skill instructions, 1,200 to semantic records, and 800 to episodes. Redistribute unused capacity. On-demand managed recall may raise the cumulative total to **8,000 tokens** and must reapply scope, freshness, and token accounting. These limits cover sofa-supplied recall, not arbitrary repository reads or harness-native skill loading; those consume the separate overall agent budget. ACP alone does not impose a hard cap on every native context read. These are tunable engineering defaults, not established optimal values. Use the profile's tokenizer when available; label fallback estimates.

An episode's failed approach can be as useful as a successful example. Include relevant counterexamples and uncertainty. Hypotheses are rendered as evidence to investigate, never as instructions. A required decision with disputed evidence must return to authoritative sources or abstain.

Log the selected IDs, revisions, scores/reasons, and context size. Track whether records were merely retrieved or actually used; retrieval frequency alone does not establish usefulness. Do not reward a record indefinitely because previous ranking already made it popular.

Mulch provides useful concrete patterns: typed Git-backed records, evidence links, file/directory anchors, BM25, scoped priming, budgets, and expiry tiers. Its local file locks and union merging do not replace coordination between separate Actions runners. Sofa can adopt these patterns in Go without requiring Mulch's runtime. [Mulch architecture](https://github.com/jayminwest/mulch)

## 4. Versioning, concurrency, privacy, and forgetting

Append episodes under unique IDs derived from repository, attempt, stage, and commit identity. Retries find the existing record. Commit changes parented to the observed state head and use non-forced reference updates; concurrent losers reload, validate, and retry. Never merge competing instructions by timestamp. Semantic and skill changes use ordinary PR conflict resolution.

Each stage records its toolkit, configuration, curated-memory, and skill revisions. Routine new guidance applies on subsequent stages using fresh snapshots; live authorization and revocation checks remain independent. Agent containers cannot directly write the ledger or shared memory branch.

Initial lifecycle defaults:

- Search episodes from the last 90 days unless a query identifies older evidence. Historical observations remain true at their recorded revision.
- Expire unpromoted hypotheses after 30 days from default retrieval; preserve explicit incident references when still relevant.
- Expire external API/service facts after 30 days. Invalidate code-derived claims when cited source blobs change, then recompute or revalidate them.
- Keep architectural decisions and approved skills until superseded, but recheck their applicability after dependent tooling or interfaces change.
- Keep redacted evidence artifacts for 14 days by default. Essential outcomes live in compact records; expired evidence is reported as unavailable.

Supersession, disputes, and tombstones take effect in retrieval and index rebuilding. Compact inactive records out of the current tree when needed and keep deterministic aggregate metrics. Archival history is not automatically injected into prompts.

Git deletion is not secure erasure: old commits, clones, and forks can retain removed content. Never intentionally store secrets or data requiring guaranteed deletion in Git. A leak requires credential revocation and explicit repository/artifact/cache remediation. [GitHub sensitive-data removal](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)

Scope memory to the calling repository by default. Public repositories' state branches and ordinary Actions artifacts must be treated as public; a `private` field creates no access boundary. Trusted repository policy must restrict what may be persisted or sent to each provider. Private downstream knowledge never flows to public sofa automatically.

The shared toolkit distributes reviewed generic skills and recipes. Downstream repositories retain their domain knowledge. A later optional private personal-memory repository may supply explicitly scoped, read-only knowledge without becoming a controller or required database. Cross-repository export is a separate, reviewed operation.

Memory poisoning is an established attack surface. A malicious source can influence which records are retrieved and then steer later behavior. Provenance, input handling, sandboxing, and deterministic policy enforcement remain necessary after storage; a second model's approval is not a security boundary. [AgentPoison](https://arxiv.org/abs/2407.12784)

## 5. Dreaming and reviewed improvement

Run a weekly consolidation opportunity, with a manual trigger. First inspect new work outcomes and later feedback/correction events since a persisted event watermark deterministically. The watermark tracks event revisions, not merely initial episode completion, so later merges or reverts remain eligible. Exclude dreaming/evaluation housekeeping by default: a dream run cannot make itself eligible for another run solely by emitting an episode. No new eligible outcomes means **zero inference**. Give admitted delivery work priority over dreaming.

Defaults: one active dream run per repository, at most 50 affected episodes per batch, 30 minutes, at most three proposals, and one open consolidation PR. Use the configured dreaming profile and its persisted inference budget; if exhausted, defer the remaining batch. Persist input event IDs and proposal fingerprints so retries update existing work.

The loop proceeds through six steps:

1. **Prepare:** join new episodes with merged/reverted PRs, owner feedback, validation outcomes, repeated failures, retries, timings, usage, and retrieval records. Detect exact duplicates and invalid citations without inference.
2. **Organize:** optionally use Jev to classify repeated incidents, rank costly patterns, or identify likely duplicate lessons from bounded candidates.
3. **Propose:** use an LLM for interpretations, semantic corrections, skill improvements, missing tests, routing changes, and opportunities to replace repeated agent work with deterministic Go recipes. Cite supporting and contradicting episodes.
4. **Evaluate:** compare no-memory, current-memory, and candidate variants against historical tasks and adversarial fixtures. Hold out cases not used to synthesize the proposal; pin code, scenarios, policy, harness, and model versions where possible. Repeat stochastic comparisons and report uncertainty.
5. **Review:** produce a PR with permitted changes, evidence, measured effects, limitations, and rollback. Kevin merges promoted semantic guidance, skills, recipes, and tests. Changes to workflow/action definitions, credentials, admission rules, gate thresholds, or provider/data-routing policy become patch/specification proposals for owner handling; an ordinary dreaming grant cannot publish them.
6. **Observe:** measure later use, success, regressions, retries, latency, runner time, and available token/cost data. Propose correction or rollback when results worsen.

Include dreaming and evaluation costs when estimating savings. Unknown provider pricing stays unknown. Do not optimize only for green checks: retain independent acceptance/security criteria. With insufficient comparable evidence, label an improvement experimental rather than claiming a proven gain.

Reflexion studies feedback-based episodic reflection; Memp investigates reusable procedures and their correction/deprecation; sleep-time compute explores preparing reusable context before later queries. These support experimentation, without guaranteeing corresponding savings in sofa's repositories. [Reflexion](https://arxiv.org/abs/2303.11366), [Memp](https://arxiv.org/abs/2508.06433), [Sleep-time Compute](https://arxiv.org/abs/2504.13171)

Configured scheduled watches may independently open bounded fix PRs under their standing authorization; Kevin still merges. Discovery drafts a specification and requires Kevin's approval before Backlog. Neither path grants dreaming permission to promote its own guidance. Dreaming cannot silently weaken gates, increase budgets, change provider/data routing, or rewrite protected authorization and promotion policies.

## 6. Minimum implementation and verification

Add Go boundaries for `MemoryReader`, `RecallBundle`, a separately privileged `ObservationWriter`, validated `MemoryProposal`, and `SkillRegistry`. Provide CLI inspection/query, validation, proposal, and dreaming operations. JSON, Markdown, and existing Git plumbing suffice for v1; no external database or embedding service is required.

Later local full-text, embedding, or relationship indexes are disposable caches keyed by corpus digest, schema, and retrieval/model versions. Rebuild them from authoritative records. Add richer linking only when evaluations show simple retrieval misses useful context; linked-note research is inspiration, not a required dependency. [A-MEM](https://arxiv.org/abs/2502.12110)

Acceptance covers concurrent/idempotent writes, interrupted finalization, source changes, expired evidence, conflicting claims, supersession, cross-repository isolation, malicious records, skill escalation, managed-recall budget enforcement, and zero-inference empty dreaming. Later feedback must trigger reconsideration, while dreaming housekeeping must not perpetuate itself. Evaluate temporal reasoning, knowledge updates, abstention, and downstream outcomes alongside retrieval recall. These are explicit long-term-memory evaluation concerns. [LongMemEval](https://arxiv.org/abs/2410.10813)
