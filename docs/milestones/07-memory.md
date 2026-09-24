# Milestone 07: Scoped memory across runs

## Outcome and prerequisites

Agents can retrieve relevant prior experience, domain knowledge, and approved skills across disposable runners without inheriting authority from that content. Observations accumulate automatically; hypotheses remain labeled; promoted guidance comes from reviewed repository revisions.

Depends on milestones **02, 04, and 05**. Read [the shared completion contract](README.md), [the main plan](../../PLAN.md), and [the memory design](../memory.md) first. Reuse the outcome events established by milestone 01, the lifecycle's later feedback events, exact-head gate evidence, and System One decision interfaces. Jev reranking is optional and disabled by default.

## Scope and exclusions

Implement storage, ingestion, retrieval, inspection, and proposal interfaces in Go. Keep the operational ledger authoritative and separate from memory. Episodic records use `episodes/` on the caller's `sofa-state` branch; reviewed semantic records use `.sofa/memory/semantic/`; procedural records use `.sofa/skills/` and bundled sofa skills.

Include provenance, status, applicability, source revision, supersession, expiry, and public/private scoping. Exclude dreaming, automatic skill promotion, cross-repository memory sharing, required embeddings/databases, and changing protected policies. This milestone does not merge proposals or release software.

## Checkpoints

1. **Contracts and continuity.** Implement versioned records, `MemoryReader`, `RecallBundle`, separately privileged `ObservationWriter`, `MemoryProposal`, and `SkillRegistry`. Map existing outcome events into episodes without inventing historical explanations or changing their identities. Distinguish event identity from episode identity so later feedback, merge, revert, release, and correction events remain independently addressable. Reject unsupported schemas clearly; any migration is explicit and idempotent.
2. **Safe ingestion.** A trusted finalizer appends observations after verifying provenance and content policy. Retain bounded agent hypotheses as unverified data. Deduplicate deliveries and use the existing non-forced Git update/retry mechanism. Agent containers cannot write authoritative storage. Interrupted work still produces an observed outcome when reconciliation has evidence.
3. **Managed recall.** Filter scope, provider, role, validity, and status before exact matching and deterministic lexical/BM25 ranking. Supply a selected read-only bundle, not the complete corpus. Enforce 4,000 initial and 8,000 cumulative sofa-managed recall tokens per stage; every subsequent recall reapplies filters and accounting. Native harness reads consume the separate overall agent budget. Optional Jev only reranks the permitted shortlist and cannot expand it.
4. **Maintenance and integration.** Add inspect/query/validate/propose CLI operations, source-change invalidation, conflicts, supersession, and expiry. Apply the memory design's 90-day episode search window, 30-day hypothesis/external-fact expiry, and 14-day evidence retention defaults. Load skills progressively from reviewed snapshots. Report selected IDs, revisions, reasons, and token estimates; missing evidence and unknown usage remain explicit.

## Observable acceptance

- [ ] **07-A:** Replay milestone 01 outcomes twice: identical observations appear once, and source identifiers remain intact.
- [ ] **07-B:** Concurrent writers preserve both independent events; crashes before/after the state update do not lose or duplicate records.
- [ ] **07-C:** Late owner feedback and a later revert produce distinct linked events without rewriting an earlier observed result.
- [ ] **07-D:** Exact/path/lexical recall finds relevant positive and negative examples. Empty results require no model call.
- [ ] **07-E:** Wrong-repository, prohibited-provider, expired, disputed, and superseded records cannot enter ordinary recall; changed source blobs invalidate applicable claims.
- [ ] **07-F:** Repeated managed recalls respect the cumulative budget. Unknown tokenizer/provider accounting is labeled rather than reported as exact.
- [ ] **07-G:** Poisoned hypotheses, malicious skill resources, and proposed working-tree guidance cannot grant permissions, waive gates, or replace reviewed skill snapshots.
- [ ] **07-H:** Public/private fixture tests demonstrate content-policy enforcement; scans are not represented as a guarantee against disclosure. Documentation explains Git deletion limitations.
- [ ] **07-I:** In an authorized disposable consumer repository, two hosted runs record and retrieve the same permitted experience through the actual state branch and selected bundle. Repeated delivery remains idempotent. This live check needs no paid/model call when reranking is disabled.

## Evidence and handoff

During implementation, write `docs/milestones/evidence/07-memory.md` with tested commits, commands, fixture results, hosted-run links, record/bundle IDs, privacy checks, and unresolved limitations. Required hosted access is an external prerequisite; local success cannot silently replace it. Do not create a completion report before implementation.

Hand milestone 09 the event cursor contract, managed-recall API, reviewed proposal path, schema fixtures, and cost/provenance records.

## Goal prompt

```text
Implement milestone 07 from docs/milestones/07-memory.md and follow docs/milestones/README.md. Verify prerequisites, implement only the specified scope, complete local/replay and required live acceptance, and record actual evidence in docs/milestones/evidence/07-memory.md. Preserve existing work and security boundaries. Report missing external prerequisites explicitly; do not waive checks, auto-merge, or release to production.
```
