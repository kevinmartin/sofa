# Milestone 01 evidence — safe hosted delivery slice

**Status: Local checks passed; live checks pending.** The disposable Project and consumer configuration are installed. Hosted acceptance still needs Kevin's issue approval and Ready move, a publisher secret, and an observed Copilot run. No hosted claim below is inferred from local tests.

## Implementation and boundaries

- Module: `github.com/kevinmartin/sofa`; target toolkit repository: `kevinmartin/sofa`; target consumer: `kevinmartin/sofa-disposable`. Source and candidate repository identities remain distinct.
- Schema v1: strict `.sofa.yml`, canonical specification digest and owner approval, orphan `sofa-state` ledger/spec snapshots, bounded candidate bundle, exact-digest check evidence, and publication intent. Opaque issue IDs are hashed into portable specification paths.
- The ledger records the owner run attempt, fence generation, cumulative budgets, checkpoint references, observations, and draft publication identity. Failure observations include the fence generation in their identity, so recovery records each failed run without rewriting the prior outcome. Concurrent equivalent observations reconcile to one record after the ledger's compare-and-swap race. The GitHub-backed state store uses non-forced ref updates and retains immutable specification snapshots.
- The worker receives only its Copilot credential and a disposable consumer checkout. ACP file callbacks are scoped to approved files, but the harness's native tools can inspect and edit that checkout; the trusted candidate validator limits what can leave it. The verifier applies candidate data in a separate checkout. The publisher accepts an exact candidate and check evidence, checks current authority/ownership before writes, and uses Git data and PR APIs without executing candidate code.
- Reusable `reconcile.yml` and `work.yml` jobs separate admission, contained execution, secretless verification, publication, and failure finalization. The prototype has one Copilot ACP adapter and one exact `gofmt` recipe. It has no discovery worker, broad board scan, System One router, specialist gates, memory search, watches, or automatic merge.

## Acceptance evidence

| ID | Current result | Evidence still required |
| --- | --- | --- |
| 01-A | Pending live | Approved disposable issue/Ready event, Copilot ACP Actions run, passing check evidence, and one draft PR URL. |
| 01-B | Local admission and claim tests pass; live pending | Observe unauthorized/duplicate deliveries and model-call telemetry in hosted path. |
| 01-C | Local ledger recovery, CAS, and publisher idempotence tests pass; live pending | Hosted cancellation/rerun and full crash matrix with run/PR references. |
| 01-D | Local stale-fence, forged-tree, and post-reference human branch-update checks pass; live pending | Hosted human branch-update canary and documented race boundary. |
| 01-E | Local path/symlink/candidate/secret-sentinel checks pass; hosted pending | Verify job credential boundaries and adversarial artifact fixture in Actions. |
| 01-F | Local ACP fake-peer/process cleanup and state budget/checkpoint tests pass; hosted pending | Observe Copilot auth/quota behavior and actual cancellation. |
| 01-G | Local exact recipe and negative-case tests pass; live pending | Confirm idle reconciliation and recipe path with zero model calls. |
| 01-H | Versioned local schemas; hosted pending | Review hosted logs/artifacts for inert secret sentinel; publish operator setup/recovery steps. |

## Local checks

On 2026-09-23, `go test ./...`, `go test -race ./...`, `go vet ./...`, and a Linux amd64 static `go build -trimpath ./cmd/sofa` passed using Go 1.24.1. The CLI integration test applies a bounded candidate to the disposable fixture, runs its `go test ./...` gate in a secretless process, and confirms the independent test file was not changed. A second CLI integration test runs the exact `gofmt` recipe without a model credential and inspects the emitted execution artifact: `used_agent=false`, `prompt_requests=0`, and `model_calls=null`. Worker-level tests also cover invalid and already-formatted recipe inputs. The local baseline fixture test intentionally fails only on the two specified greeting cases. Workflow YAML parses locally; actual GitHub Actions behavior remains untested.

## Live resources and remaining setup

- The current canary pins immutable toolkit commit `b398279a8efe557b8fabb5cb76d94a1c329849eb` in [draft sofa PR #1](https://github.com/kevinmartin/sofa/pull/1). The disposable consumer's initial fixture commit is `74304f4` on `main`; `go test ./...` fails at baseline only for the two intended greeting cases. Consumer commit `8ef5d7f39ba4469ecbec1d1f54849bd80ae509cb` pins both reusable workflow references and `toolkit_sha` to the same toolkit commit and passes the publisher secret explicitly.
- [Disposable issue #1](https://github.com/kevinmartin/sofa-disposable/issues/1) has the exact fixture specification from `examples/consumer/issue-body.md`. Its canonical digest is `cf358e96c2ddc9e03d6b6f7ff2025637507f508617642267e8d9467213205673`. **It is not approved or Ready.** Kevin must post the approval comment and perform the Ready transition personally.
- [Disposable Project #2](https://github.com/users/kevinmartin/projects/2), node ID `PVT_kwHOAAfL7c4Bke1I`, is open and private. Its Status field has a `Ready` option. Issue #1 is an active item in `Todo`; the issue's Project and status-history GraphQL queries succeeded before approval. Consumer `.sofa.yml` was installed with that immutable Project ID in commit `7da67198f7e5c609e8c2b5e18a588f90e39c63fe`. The repository secret list confirms `SOFA_PROJECTS_TOKEN` exists, without exposing its value. The GitHub CLI is authenticated as `kevinmartin` with Project access when run with network permission.
- Hosted execution uses the consumer's ephemeral `GITHUB_TOKEN` for controller/finalizer state writes and Copilot Requests. A separate `SOFA_PUBLISH_TOKEN`, scoped to only `sofa-disposable` with Contents and Pull requests write permission, is required for publication-stage ledger updates, candidate branch creation, and draft PR creation in the trusted publisher job; it is not yet configured. The repository default Actions token is read-only. The repository-wide toggle that also allows Actions to approve PR reviews remains disabled. No Project or publisher credential enters the agent container; only the dedicated Copilot credential does.
- Finite trial: one approved issue, one ten-minute agent attempt, one reserved ACP prompt per attempt, zero repair attempts, and one infrastructure retry. Copilot's internal model-call count and actual cost are unknown when ACP does not report them. Actual request, duration, run ID, candidate commit, and PR observations remain pending the live run.

## Reproduction and handoff

The toolkit branch and manual consumer caller are installed and pinned. Kevin must configure `SOFA_PUBLISH_TOKEN` as a disposable-repository secret using a fine-grained PAT restricted to that repository with Contents and Pull requests write permissions. A short-lived App installation token cannot be stored directly as a durable secret; App support would need token minting in the trusted job. He then posts `/sofa approve-spec cf358e96c2ddc9e03d6b6f7ff2025637507f508617642267e8d9467213205673` on issue #1 and moves it into `Ready`. Manually run **sofa disposable canary** on `main` with issue number `1`. The admitted manifest artifact is `sofa-manifest-<run>-<attempt>`, the retained candidate is `sofa-verified-candidate-<run>-<attempt>`, and trusted check evidence is `sofa-evidence-<run>-<attempt>`. After a terminal cancellation, rerun the same issue; the ledger must reclaim or suppress it based on the recorded phase and fetch only the checkpoint's producing run. Record the actual run, candidate, PR, and recovery observations here. Leave any resulting PR as a draft for Kevin to review and merge.

Automatic approval review rejected enabling the repository-wide Actions PR-review approval setting because it grants persistent bot approval capability beyond this draft-PR canary. The safer publisher-token path above leaves that setting disabled.
