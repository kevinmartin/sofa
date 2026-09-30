---
name: sofa-hosted-gate
description: Diagnose or recover sofa PR quality-policy and hosted-e2e gates, including pending or stale statuses, failed disposable suites, provenance, and cleanup. Use for an exact PR head/base gate investigation; not for ordinary local test failures or routine documentation edits.
---

# Sofa hosted gate

Read [AGENTS.md](../../../AGENTS.md),
[quality-and-config.md](../../../docs/quality-and-config.md), and the relevant
[01.1 evidence](../../../docs/milestones/evidence/01.1-e2e-regression-harness.md).
The [gate setup](../../../docs/milestones/01.1-gate-setup.md) records historical
bootstrap configuration; verify current workflows and rules before relying on it.
This skill grants no new external write, credential, spending, or merge authority.

## Establish current evidence

1. Record repository, PR number, current head SHA, base SHA, required check names
   and source Apps, and the latest status/check runs for that head. The current
   contract includes `quality / result`, `sofa / quality-policy`, and
   `sofa / hosted-e2e`; verify actual rules rather than using a retired check name.
2. Trace `.github/workflows/pr-gate-bridge.yml` and `cmd/sofa-test/bridge.go` from
   trusted sofa `main`. Inspect the current `sofa-gate.yml` workflow, coordinator,
   and observer in the designated `kevinmartin/sofa-disposable` repository. Keep
   candidate code separate from the trusted bridge's credentials and execution.
3. Follow linked runs and artifacts. Match the PR head/base, suite identity,
   producer/recovery run, artifact generation, and bundle digest using the current
   report schema. Read the relevant failing job logs with credentials redacted.
   A completed Actions run, old report, or user-posted status cannot substitute
   for current evidence from the expected App. Re-read the PR pair before a final
   conclusion, because another commit or base update may make evidence stale.

## Diagnose before retrying

- Separate quality-job failure, workflow-contract rejection, missing bridge
  dispatch, coordinator/admission failure, candidate/verification failure,
  observer rejection, and cleanup failure. Locate the first concrete failure.
- For policy rejection, compare candidate workflow bytes to the exact trusted
  base under the current policy. Only existing action-line SHA/version-comment
  changes are admitted in the reusable workflow; the caller must match. Inspect
  full-SHA pin validity and base freshness. Do not bypass policy, add blanket pin
  approvals, or run candidate code with trusted credentials to clear a status.
- A different PR's bad pin or suite failure must not prevent this PR's valid suite
  from running. Check per-PR isolation before assuming the target itself failed.
- Inspect existing attempts before dispatching. Prefer an existing matching run.
  If recovery is already authorized, use the current workflow's targeted PR input
  and exact revision checks within configured finite budgets. Do not repeatedly
  dispatch unchanged failures, broaden credentials, reset aggregate budgets, or
  create duplicate suite resources.
- Clean up only resources proven to belong to the suite, using its verified
  identities and cleanup receipt. Preserve failed evidence needed for diagnosis;
  never sweep unrelated issues, branches, PRs, or Project items by name alone.

## Report and stop

Report the exact pair, failure stage/cause, relevant run/artifact links, verified
status source, recovery performed, and cleanup result. State missing evidence or
external prerequisites explicitly. Stop when the current pair has a terminal
result or a concrete blocker; a later scheduled follow-up needs task authorization.
Local fake ACP checks and the secretless hosted surrogate have limited scope;
never label them a live-provider or full production acceptance run. Never post a
manual success status, relax a required gate, merge sofa, or move a release tag as
an investigation shortcut. Disposable test authority is confined to its task's
named resources and does not transfer to sofa's merge/release policy.
