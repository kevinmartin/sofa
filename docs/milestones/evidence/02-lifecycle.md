# Milestone 02 evidence — Discovery and lifecycle

**Status: In progress.** The lifecycle implementation and live acceptance checks
are not complete. This report distinguishes implemented code, local fixtures,
and hosted observations; it is not a claim that a draft specification has been
owner-approved or a candidate has passed required release gates.

## Current implementation and decisions

- Work branch: `codex/milestone-02-lifecycle` in a managed worktree created from
  current `main` (`e5ed5b07fb81d20ec84916d05d8aed7ae274f888`). It includes
  the repository's new `AGENTS.md`; unrelated changes in the primary checkout
  were left untouched.
- `.sofa.yml` lifecycle configuration is opt-in for delivery-only v1 consumers.
  It maps all ten canonical statuses, defaults to a ten-minute poll and WIP 2/1,
  supports a 60-minute economy setting, and names post-merge checks together
  with their trusted GitHub App IDs. Runtime and JSON Schema validation align.
- The designated private `sofa-disposable` Project is
  `PVT_kwHOAAfL7c4Bke1I`. Its built-in Status field
  `PVTSSF_lAHOAAfL7c4Bke1IzhjPQ38` was updated to the ten canonical display
  names. Existing option IDs for former Todo, Ready, In Progress, and Done were
  preserved; those names now map to Inbox, Ready, Building, and Done. The
  resulting option IDs are recorded below. This changes only the delegated
  disposable Project, not Sofa's merge policy.

| Canonical stage | Option ID |
| --- | --- |
| Inbox | `f75ad846` |
| Discovery | `97253131` |
| Spec Review | `0efcde37` |
| Backlog | `aa6c2b7a` |
| Ready | `e4557284` |
| Building | `47fc9ee4` |
| Verification | `3e439866` |
| Review | `ac306ec4` |
| Release | `5afdccbe` |
| Done | `98236657` |

## Acceptance ledger

| ID | Observed result | Evidence and remaining proof |
| --- | --- | --- |
| 02-A | Pending | No live Discovery specification or Kevin Backlog approval yet. |
| 02-B | Pending | Ready admission is still the milestone 01 path; revision-bound Backlog approval is under implementation. |
| 02-C | Partial | Table-driven transition and isolated-item replay tests pass locally; no hosted Project write has been exercised. |
| 02-D | Partial | Local coalesced polling, pending retry, WIP, and no-inference tests pass; the scheduled consumer caller is not installed. |
| 02-E | Partial | Local owner-review, same-PR lease, repair-budget, and interrupted-admission recovery tests pass. Recovery requires exact terminal-run proof and retains charged counters; a prepared publication remains for operator reconciliation. The hosted repair workflow is absent. |
| 02-F | Partial | Local missing/wrong-candidate gate tests hold work; no hosted milestone 04 gate evidence is available. |
| 02-G | Partial | Read-only observation of designated, already-merged disposable PR #178 reached `done` for the exact merge commit and required `wake / status` check. Local replay covers missing, failed, wrong-App, and successful checks plus closed-unmerged; hosted post-merge smoke is not configured. |
| 02-H | Partial | Local later-feedback and exact-inverse-revert append-only tests pass; hosted correction observation is unproved. |

## Local and live checks

- `go test ./internal/config -count=1` passed after lifecycle schema and policy
  changes. This proves configuration shape and representative negative cases,
  not lifecycle behavior.
- GitHub GraphQL returned the private Project and its updated Status options.
  No model inference or paid profile was used for that administrative setup.
  Cost and runtime usage are not measured yet.
- GraphQL listed six enabled disposable Project automations: Auto-add sub-issues,
  Auto-close issue, Item added, Item closed, Pull request linked, and Pull
  request merged. The public `ProjectV2Workflow` API exposes no destination
  Status option, so this does not establish whether Backlog or Ready is
  protected from an automatic move. A signed-in Project settings UI inspection
  remains required before counting live Backlog approval as trusted.
- The integrated local run `GOCACHE=/private/tmp/sofa-milestone02-gocache go test -count=1 ./...` passed on September 29, 2026, including the existing `httptest` package with local-port permission. `go vet ./...`, `go mod tidy -diff`, and `git diff --check` passed. These include replay fixtures, not a hosted lifecycle run. The installed `staticcheck` cannot decode Go 1.27 export data; hosted quality validation remains needed.
- `actionlint` v1.7.12 parsed `.github/workflows/lifecycle.reusable.yml` without findings, using the cached module source and Go 1.27.1. ShellCheck is not installed locally; hosted quality validation is still needed.
- Read-only `sofa-test release-observe --repository kevinmartin/sofa-disposable --pr 178 --expected-merge-sha 9d4dc05724e55a9b090e0895abbf24ff5be97fed --required-check 'wake / status@15368'` returned `outcome=done`. [PR #178](https://github.com/kevinmartin/sofa-disposable/pull/178) is closed and merged, its merge commit matches that SHA and the disposable default branch head, and GitHub reports a successful exact-commit `wake / status` check from Actions App 15368. This is a designated merged fixture; sofa did not generate or merge it as part of milestone 02.

## Remaining prerequisites and recovery

The consumer's committed `.sofa.yml` still has the delivery-only policy, and
its caller does not yet run lifecycle reconciliation. The current disposable
configuration also sets `repair_attempts: 0`, so a hosted 02-E repair canary
will require a reviewed consumer policy change before the owner review is
submitted. Live Discovery requires a
new designated disposable issue and a human move from Spec Review to Backlog;
sofa will not make that approval move. Project write access and automations must
remain restricted so untrusted actors cannot set Backlog or Ready. Re-run local
tests and inspect exact Project/issue revisions after code integration. Later
release checks require a merged fixture selected without merging a sofa PR.
The already-merged PR #178 provides the successful release-observation fixture;
it does not prove the hosted lifecycle handoffs.

Automatic approval review rejected adding the credentialed Discovery CLI and
review repair workflow, classifying those Project/comment/PR side effects as
outside the latest Dependabot question despite the active milestone 02 goal.
The implementing agents did not retry those edits through another path.
Discovery revision/re-admission also remains incomplete. These are functional
gaps, not waived acceptance checks; this report must stay **In progress** until
they are implemented and validated.

## Handoff

Downstream milestone work may rely only on completed, validated interfaces
recorded here when this report reaches **Complete**. Until then, milestone 01's
delivery grant and draft-only publication remain the demonstrated behavior.
