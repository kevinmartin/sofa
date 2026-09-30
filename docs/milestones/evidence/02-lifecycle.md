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
| 02-B | Partial | Local v1→v2 replay requires changed source, a later owner Discovery move, a new bot comment, a new Spec Review observation, a later Backlog approval, and a later Ready admission. The old attempt is fenced and its budget carries forward. No live revision has been exercised. |
| 02-C | Partial | Table-driven transition and isolated-item replay tests pass locally; no hosted Project write has been exercised. |
| 02-D | Partial | Local coalesced polling, pending retry, Discovery/delivery WIP, and no-inference tests pass. The reusable lifecycle workflow now dispatches bounded Discovery and Ready lists; the scheduled consumer caller is not installed. |
| 02-E | Partial | Local owner-review, same-PR lease, repair-budget, and interrupted-admission recovery tests pass. The reusable repair workflow separates model, verifier, publisher, and failure-finalizer credentials. Recovery requires exact terminal-run proof and retains charged counters; a prepared publication remains for operator reconciliation. No hosted repair has run. |
| 02-F | Partial | Local missing/wrong-candidate gate tests hold work; no hosted milestone 04 gate evidence is available. |
| 02-G | Partial | Read-only observation of designated, already-merged disposable PR #178 reached `done` for the exact merge commit and required `wake / status` check. Local replay covers missing, failed, wrong-App, and successful checks plus closed-unmerged; hosted post-merge smoke is not configured. |
| 02-H | Partial | Lifecycle reconciliation now reads later PR comments and bounded default-branch revert candidates for a Done item whose exact merged PR passed configured release checks. Local two-PR replay proves a transient read failure cannot borrow another PR's comments or erase a prior Done event; the verified inverse check, append-only replay, and no-raw-feedback tests pass. Hosted correction observation is unproved. |

## Local and live checks

- `go test ./internal/config -count=1` passed after lifecycle schema and policy
  changes. This proves configuration shape and representative negative cases,
  not lifecycle behavior.
- GitHub GraphQL returned the private Project and its updated Status options.
  No model inference or paid profile was used for that administrative setup.
  Cost and runtime usage are not measured yet.
- Signed-in Project settings showed six enabled automations on September 30.
  `Item added to project` placed issues and PRs in Inbox; `Auto-close issue`
  closed issues on Done; `Item closed` placed issues and PRs in Done; `Pull
  request linked to issue` placed items in Building; and `Pull request merged`
  placed items in Done. The four completion/building automations were disabled
  in the delegated disposable Project so they cannot bypass Sofa's adjacent
  transition and release checks. `Item added to project` and `Auto-add
  sub-issues to project` remain enabled; settings now shows **2 enabled**.
  The Project is private and its Manage access page lists **0 collaborators**.
  GitHub's public GraphQL workflow object did not expose these destination
  Status options, so this conclusion required the signed-in UI inspection.
- The integrated local run `GOCACHE=/private/tmp/sofa-milestone02-gocache go test -count=1 ./...` passed on September 29, 2026, including the existing `httptest` package with local-port permission. `go vet ./...`, `go mod tidy -diff`, and `git diff --check` passed. These include replay fixtures, not a hosted lifecycle run. The installed `staticcheck` cannot decode Go 1.27 export data; the hosted Go quality job passed on the code revision below.
- After wiring completion corrections, the integrated `go test -count=1 ./...`, `go vet ./...`, and `git diff --check` passed again. Revert discovery reads at most the latest 100 commits at the observed default-branch head and uses standard revert messages only as hints; exact inverse verification remains mandatory. This bound can miss older or unlabeled reversions.
- `actionlint` v1.7.12 parsed `.github/workflows/lifecycle.reusable.yml` without findings, using the cached module source and Go 1.27.1. ShellCheck is not installed locally; the hosted GitHub Actions quality job passed on the code revision below.
- After integrating the credentialed Discovery and review-repair workflows plus the same-issue revision path, the full local Go suite and `go vet ./...` passed on September 30, 2026. The first full test attempt failed only because the sandbox denied an existing `httptest` loopback listener; the authorized local-port rerun passed. Targeted race tests for `internal/state`, `internal/discovery`, `internal/lifecycle`, and `cmd/sofa` passed. `actionlint` v1.7.12 parsed all three new/changed reusable workflows with the existing exact-path Copilot permission exception; `git diff --check` passed. These are local validations, not hosted milestone 02 acceptance.
- Draft [sofa PR #18](https://github.com/kevinmartin/sofa/pull/18) code revision `2a18884fb512ae2db40d66cfd140f86825258a2c`, base `e5ed5b07fb81d20ec84916d05d8aed7ae274f888`, passed the hosted `quality / go`, `quality / github-actions`, `quality / result`, and App-sourced `sofa / quality-policy` checks. The established fake-ACP [hosted suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36662911037) passed execute, secretless verify, controlled publication interruption, retained-candidate recovery, Project completion, suite-owned cleanup, and App status publication. Its [redacted cleaned artifact](https://github.com/kevinmartin/sofa-disposable/actions/runs/36662911037/artifacts/11075590062) binds that exact pair, disposable base `9d4dc05724e55a9b090e0895abbf24ff5be97fed`, digest `718ea37cb6ae417e7fedc58d2de7a0b6be0e624a1155d0f348a952383ccc7dc6`, producer `36662523444`, recovery `36662751308`, one fake prompt, zero provider requests, closed issue #206 and draft PR #207, archived Project item, and absent suite refs. `kevins-sofa[bot]` posted `sofa / hosted-e2e` success on the exact head. This validates the existing delivery-gate surrogate, **not** live milestone 02 Discovery, review repair, or release handoffs. A subsequent evidence-only commit requires its own exact-head gate result before the draft PR can be considered green again.
- Read-only `sofa-test release-observe --repository kevinmartin/sofa-disposable --pr 178 --expected-merge-sha 9d4dc05724e55a9b090e0895abbf24ff5be97fed --required-check 'wake / status@15368'` returned `outcome=done`. [PR #178](https://github.com/kevinmartin/sofa-disposable/pull/178) is closed and merged, its merge commit matches that SHA and the disposable default branch head, and GitHub reports a successful exact-commit `wake / status` check from Actions App 15368. This is a designated merged fixture; sofa did not generate or merge it as part of milestone 02.

## Remaining prerequisites and recovery

The consumer's committed `.sofa.yml` still has the delivery-only policy, and
its caller does not yet run lifecycle reconciliation. A disposable branch is
being prepared with the lifecycle policy, a one-repair limit, and callers
pinned to the eventual reviewed Sofa commit; it has not been published or
merged. Live Discovery requires a
new designated disposable issue and a human move from Spec Review to Backlog;
sofa will not make that approval move. Project write access and automations must
remain restricted so untrusted actors cannot set Backlog or Ready. Re-run local
tests and inspect exact Project/issue revisions after code integration. Later
release checks require a merged fixture selected without merging a sofa PR.
The already-merged PR #178 provides the successful release-observation fixture;
it does not prove the hosted lifecycle handoffs.

The owner subsequently authorized the credentialed Discovery CLI/workflow,
same-PR review repair workflow, and same-issue spec revision/re-admission
explicitly. Those implementations now pass local tests, but their hosted
acceptance remains in progress. Supersession preserves the old draft PR and
its publication evidence while fencing its worker; it does not close that PR
or rewrite its checks. The owner may inspect or close the stale PR separately.
These are not waived acceptance checks; this report must stay **In progress**
until the live paths are validated.

## Handoff

Downstream milestone work may rely only on completed, validated interfaces
recorded here when this report reaches **Complete**. Until then, milestone 01's
delivery grant and draft-only publication remain the demonstrated behavior.
