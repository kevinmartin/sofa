# Milestone 02 evidence — Discovery and lifecycle

**Status: In progress.** The lifecycle implementation and live acceptance checks
are not complete. This report distinguishes implemented code, local fixtures,
and hosted observations; it is not a claim that a draft specification has been
owner-approved or a candidate has passed required release gates.

## Current implementation and decisions

- Work branch: `codex/milestone-02-lifecycle` in a managed worktree created from
  `main` (`e5ed5b07fb81d20ec84916d05d8aed7ae274f888`) and rebased onto
  current `main` (`177a29b2076e77310f481ca307fe8a1b633de8e8`). It includes
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
| 02-A | Partial | Live issue #215 reached model execution but produced no specification file; the failure was blocked without a comment or approval. Corrected issue #219 produced a versioned specification with stated evidence and acceptance examples, and the reconciler moved it to Spec Review. Its exact comment/digest is recorded without approval; Kevin's Backlog move remains pending. |
| 02-B | Partial | Local replay covers changed source, a later Discovery move, a new bot comment, a new Spec Review observation, and fresh Backlog/Ready approval. Live disposable issue #280 now proves the same-issue unapproved-proposal reset: a material source edit and later Discovery move produced a distinct second comment, and the old proposal entered reset history while the two model calls remained charged against a two-call aggregate cap. The current comment is back in Spec Review with no approved digest; Kevin's Backlog and Ready moves remain untested. |
| 02-C | Partial | Table-driven transition and isolated-item replay tests pass locally. The hosted controller moved the published #219 specification and, later, each current #280 proposal to Spec Review without moving either item to Backlog. A hosted poll wrote bounded, issue-specific hold reasons and next actions to separate Project fields without moving #219; later lifecycle stages and adverse human edits lack hosted evidence. |
| 02-D | Partial | Local coalesced polling, pending retry, Discovery/delivery WIP, no-inference, and changed/unchanged Backlog advisory tests pass. Reconciliation reports fixed, redacted re-review or owner-field suggestions without a model call or Project move. A live manual wake on roughly 40 Project items dispatched no unauthorized Ready work; after skipping remote approval reads for items with no recorded spec, the next wake finished in 49 seconds instead of 4m11s. A later 1m33s hosted wake used a prebuilt exact-SHA controller, wrote Project advice, and dispatched no unauthorized Ready work. Subsequent #280 manual wakes admitted the initial proposal, held a revised proposal at the exhausted one-call cap, and dispatched it only after an explicit two-call policy change. Two earlier scheduled ticks failed before jobs began because the caller pinned an unreachable Sofa commit. A successful actual scheduled tick at the current pin remains unobserved. |
| 02-E | Partial | Local owner-review, same-PR lease, repair-budget, and interrupted-admission recovery tests pass. The reusable repair workflow separates model, verifier, publisher, and failure-finalizer credentials. Recovery requires exact terminal-run proof and retains charged counters; a prepared publication remains for operator reconciliation. No hosted repair has run. |
| 02-F | Partial | Local missing/wrong-candidate gate tests hold work; no hosted milestone 04 gate evidence is available. |
| 02-G | Complete | Disposable PR #252 installed a dedicated push-to-main smoke gate and configured its exact name and GitHub Actions App ID. Read-only observation of its exact merge commit reported `release-blocked` while the check was pending and `done` after success. A closed-unmerged canary returned `closed-unmerged`; live missing/wrong-App probes blocked, and local replay covers failed release and other adverse cases. |
| 02-H | Partial | Lifecycle reconciliation now reads later PR comments and bounded default-branch revert candidates for a Done item whose exact merged PR passed configured release checks. Local two-PR replay proves a transient read failure cannot borrow another PR's comments or erase a prior Done event; the verified inverse check, append-only replay, and no-raw-feedback tests pass. Hosted correction observation is unproved. |

## Local and live checks

- `go test ./internal/config -count=1` passed after lifecycle schema and policy
  changes. This proves configuration shape and representative negative cases,
  not lifecycle behavior.
- GitHub GraphQL returned the private Project and its updated Status options.
  No model inference or paid profile was used for that administrative setup.
  Hosted run durations below are measured; ACP does not expose provider token
  counts, so model-token cost remains unknown.
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
- An earlier [Sofa PR #18](https://github.com/kevinmartin/sofa/pull/18) code revision `2a18884fb512ae2db40d66cfd140f86825258a2c`, base `e5ed5b07fb81d20ec84916d05d8aed7ae274f888`, passed the hosted `quality / go`, `quality / github-actions`, `quality / result`, and App-sourced `sofa / quality-policy` checks. The established fake-ACP [hosted suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36662911037) passed execute, secretless verify, controlled publication interruption, retained-candidate recovery, Project completion, suite-owned cleanup, and App status publication. Its [redacted cleaned artifact](https://github.com/kevinmartin/sofa-disposable/actions/runs/36662911037/artifacts/11075590062) binds that exact pair, disposable base `9d4dc05724e55a9b090e0895abbf24ff5be97fed`, digest `718ea37cb6ae417e7fedc58d2de7a0b6be0e624a1155d0f348a952383ccc7dc6`, producer `36662523444`, recovery `36662751308`, one fake prompt, zero provider requests, closed issue #206 and draft PR #207, archived Project item, and absent suite refs. `kevins-sofa[bot]` posted `sofa / hosted-e2e` success on that exact head. This validates the existing delivery-gate surrogate, **not** live milestone 02 Discovery, review repair, or release handoffs. Each later PR head requires its own exact-head gate result.
- Disposable [PR #213](https://github.com/kevinmartin/sofa-disposable/pull/213)
  installed the lifecycle, Discovery, and review callers plus opt-in consumer
  policy; [PR #218](https://github.com/kevinmartin/sofa-disposable/pull/218)
  updated all exact toolkit pins after the first live fixes. Both were merged
  within the delegated disposable repo. The first metadata-only
  [poll](https://github.com/kevinmartin/sofa-disposable/actions/runs/36729919450)
  passed in 4m11s on roughly 40 Project items and dispatched no Ready issue.
  A ledger precheck skipped remote approval reads for items without specs; the
  next [poll](https://github.com/kevinmartin/sofa-disposable/actions/runs/36732317712)
  passed in 49s with no unauthorized dispatch. The first-time projection writes
  also contributed to the first poll, so this is an observed end-to-end delta,
  not an isolated benchmark of the precheck.
- [Issue #215](https://github.com/kevinmartin/sofa-disposable/issues/215)
  was owner-admitted to Discovery under the delegated disposable grant. Its
  [run](https://github.com/kevinmartin/sofa-disposable/actions/runs/36730636430)
  reserved one model turn but the ACP agent did not edit `spec.md`; the bounded
  finalizer marked the task blocked, published no comment, and did not set an
  approval status. The worker prompt was then changed to explicitly request
  ACP file read/edit rather than a chat proposal, with only non-sensitive tool
  counters in no-output errors. The next issue was used because #215's one-call
  budget was retained.
- [Issue #219](https://github.com/kevinmartin/sofa-disposable/issues/219)
  had a fresh private Project Discovery admission. Its
  [run](https://github.com/kevinmartin/sofa-disposable/actions/runs/36732538728)
  made one bounded ACP turn, produced a validated v1 specification, and posted
  [comment #5913836437](https://github.com/kevinmartin/sofa-disposable/issues/219#issuecomment-5913836437)
  through the credentialed publisher after revalidating Project and issue
  identity. [Handoff](https://github.com/kevinmartin/sofa-disposable/actions/runs/36733163354)
  moved that item to Spec Review; a second
  [poll](https://github.com/kevinmartin/sofa-disposable/actions/runs/36733485053)
  captured the immutable comment digest
  `a7470245026479bcf6f22d68365155d411cbc38170d633f25d5175c417983bef`
  in the trusted ledger with no `approved_digest`. The specification cites the
  admitted idea and bounded repository facts; it explicitly marks source-code
  details it could not inspect as unverified. Kevin has not approved Backlog.
- The hosted gate on former Sofa head `c9f459c19945502a66d54f23c65bb66fec0b7ce2`
  correctly posted a failure when PR #18's base remained at `e5ed5b0` after
  `main` advanced to `e1d29cb` through Dependabot PR #11. The trusted observer
  requires the PR base to equal current main before comparing
  `e2e-fake.yml`; it did not accept the stale base or weaken the pin rule.
  PR #18 was rebased onto current main. The newer head-specific results are
  recorded below.
- An earlier read-only observation of [disposable PR #178](https://github.com/kevinmartin/sofa-disposable/pull/178) returned `done` for its exact merge commit `9d4dc05724e55a9b090e0895abbf24ff5be97fed` and then-successful `wake / status@15368` check. Later unrelated hosted-gate runs wrote skipped checks of the same name and App on that commit; the observer correctly now returns `release-blocked` because it selects the latest check. This transient gate result is not durable post-merge smoke evidence.
- The previous review-ready PR head `e8e4ac9957de40c12353cf1c733dac7923ea0c1d` passed the App-authored [hosted E2E recovery suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36735984564) after the stale-base correction; CodeRabbit and Codex then reviewed that head. Their ten inline findings led to scheduled-caller, Project item isolation, bounded Done patrol, Discovery reset/supersession, and owner-review/inline-feedback fixes. The later owner `COMMENTED` review is informational, not a formal withdrawal of `CHANGES_REQUESTED`; a later approval or dismissal ends that repair authority. Local `go test -count=1 ./...`, `go vet ./...`, `go mod tidy -diff`, `actionlint` on all workflows, and targeted `go test -race` for state, Discovery, review, and CLI passed for the integrated fixes. Head `d0e0f27c65ed5c7e302b2533692b17b892f84cdc` then passed [hosted E2E](https://github.com/kevinmartin/sofa-disposable/actions/runs/36766288795) and CodeRabbit's first re-review.
- CodeRabbit's requested full review of `d0e0f27c65ed5c7e302b2533692b17b892f84cdc` identified seven further edge cases. The follow-up preserves limits for exact legacy-attempt replays, requires release evidence and a merged PR before Done, rechecks pending moves against fresh evidence, accepts owner resets back to Discovery, skips unrelated oversized issue comments, and permits publication only at a recorded factory delivery stage. The full Go suite, vet, module diff, actionlint, and diff checks pass locally. That revision was superseded by the reviewed head recorded below.
- After further fixes, Sofa PR #18 head `8ee343c441c81ff0eb35ca4bcfdb10bce48c0513` against base `177a29b2076e77310f481ca307fe8a1b633de8e8` passed `quality / result`, App-authored `sofa / quality-policy`, CodeRabbit's requested full review, and the exact-head [hosted E2E suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36799316386). That review found three additional recoverability and validation edge cases. The follow-up code head `66cb27eca1411abe6d8bd1a69b33a0fae410b106` passed the full Go suite, vet, staticcheck, module diff, changed-file formatting, CodeRabbit, and its own exact-head [hosted E2E suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36801021539). This evidence-document update creates another PR head that requires its own exact-head gate before merge.
- Disposable scheduled [run 36770147891](https://github.com/kevinmartin/sofa-disposable/actions/runs/36770147891) and [run 36793528564](https://github.com/kevinmartin/sofa-disposable/actions/runs/36793528564) failed before creating jobs because the caller's `c9f459c` reusable-workflow reference was no longer reachable after a Sofa branch rewrite. Merged [disposable PR #247](https://github.com/kevinmartin/sofa-disposable/pull/247) pins lifecycle, Discovery, and review callers and matching `toolkit_sha` inputs to reachable `8ee343c`; all three referenced workflows resolved through GitHub and caller actionlint passed. The first [manual wake](https://github.com/kevinmartin/sofa-disposable/actions/runs/36799810049) then correctly refused to scan because the designated Project had become public. Project #2 was restored to private; the [next manual wake](https://github.com/kevinmartin/sofa-disposable/actions/runs/36800029723) succeeded. The next actual scheduled tick still requires observation. This private-Project check was preserved.
- Merged [disposable PR #251](https://github.com/kevinmartin/sofa-disposable/pull/251) refreshed the separate delivery caller's reusable-workflow and `toolkit_sha` pins to reachable Sofa head `66cb27eca1411abe6d8bd1a69b33a0fae410b106`. Actionlint, exact-commit lookup, and CodeRabbit passed. A [manual delivery wake for issue #219](https://github.com/kevinmartin/sofa-disposable/actions/runs/36802329689) resolved and built the trusted controller, then failed admission because the issue remains in Spec Review; its `work` job was skipped. It did not spend a model turn or publish a PR. The refusal is expected, though the current error text says `discovery lacks trusted Project authority` rather than naming the missing Ready approval.
- Merged [disposable PR #252](https://github.com/kevinmartin/sofa-disposable/pull/252) added a uniquely named, read-only push-to-main fixture smoke workflow and configured `release.required_checks` with `sofa-post-merge-smoke@15368`. Local `go test ./...`, actionlint, diff checks, and CodeRabbit passed. GitHub started [run 36802506523](https://github.com/kevinmartin/sofa-disposable/actions/runs/36802506523) on exact merge commit `4954f762917fd3067ed427a3bfd04d94b7d93ca4`, and its `sofa-post-merge-smoke` job succeeded. Read-only `sofa-test release-observe --repository kevinmartin/sofa-disposable --pr 252 --expected-merge-sha 4954f762917fd3067ed427a3bfd04d94b7d93ca4 --required-check 'sofa-post-merge-smoke@15368'` returned `release-blocked` during the run, then `done` after success. The same observer returned `closed-unmerged` for [disposable PR #250](https://github.com/kevinmartin/sofa-disposable/pull/250); missing-check and wrong-App probes on PR #178 blocked instead of passing. No Sofa PR was merged for this evidence.
- Sofa PR #18 head `0aba0f9acad5e6233bc955211b1d4421d2753b06` passed `quality / result`, `sofa / quality-policy`, and the exact-head [hosted E2E recovery suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36803424898). The suite intentionally interrupted its first publication, retained the candidate, reverified it on the recovery run without a second execute, completed its disposable Project/cleanup stages, and posted App-authored `sofa / hosted-e2e` success. The PR remains unmerged for Kevin's decision.
- A local acceptance audit found that changed Backlog inputs were reduced to bare held issue numbers. The controller now compares canonical source digests for recorded Backlog proposals, treats an edited comment/revision as a re-review suggestion, and reports owner-field or dependency gaps as bounded, fixed-text `backlog_advisories` in the result and Actions summary. It never moves Backlog/Ready or emits raw issue/comment content. Focused cases cover unchanged and changed/invalid sources, mismatched identities, missing/malformed owner fields, and incomplete/Done dependencies. The full Go suite, vet, staticcheck, module diff, formatting, actionlint, and diff checks passed locally. The first full-suite attempt hit the sandbox's existing `httptest` loopback restriction; a permitted rerun passed. Hosted Backlog advice remains unproved while the only new specification is in Spec Review.
- Sofa PR #18 head `6ca9f93fb1aa1546448ad8143d3ea5801516e237` passed quality-policy, CodeRabbit's requested full review, and the exact-head [hosted E2E recovery suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36805440484). CodeRabbit's four findings on that head were checked against the implementation: cancelled repair-stage finalization, immediate Spec Review capture after a recovered move, single-read approved-spec verification, and schema/runtime agreement for optional Project field names. The focused fixes and regression cases passed the full Go suite, vet, staticcheck, module diff, formatting, and actionlint locally. All four threads received evidence-backed replies and were resolved. Follow-up head `e2dc8676a2b49efc7e5d5e3c69bd48572dc19b7c` passed required quality checks and its own [hosted E2E recovery suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36806932341), with no second execute on recovery. This evidence update creates another head requiring an exact-head gate.
- Merged [disposable PR #257](https://github.com/kevinmartin/sofa-disposable/pull/257) pinned its lifecycle, Discovery, and review callers to reachable `e2dc8676a2b49efc7e5d5e3c69bd48572dc19b7c`; actionlint and CodeRabbit passed. A [manual lifecycle wake](https://github.com/kevinmartin/sofa-disposable/actions/runs/36807192684) then built that toolkit revision and completed reconciliation, the fixed-text Backlog summary, and both bounded dispatch steps successfully. It did not exercise an owner-approved Backlog or Ready transition; the first actual scheduled run after the caller-pin repair remains unobserved.
- A follow-up acceptance audit found that terminal Blocked/Deferred attempts appeared as generic held issue numbers in lifecycle output. Reconciliation now preserves those validated phases in bounded, fixed-text hold advice. It keeps raw failure details out of Actions summaries and isolates advice by current attempt and Project identity. Focused tests cover two items, supersession, and text redaction. Hosted confirmation is pending.
- The lifecycle poll previously checked out and rebuilt Sofa on every wake. A secretless, default-branch controller-build workflow now produces a 30-day artifact for an exact toolkit SHA. The poll discovers it in the consumer repository and checks producer run/repository/branch/history, archive digest, manifest, and binary digest before executing it with the Project credential. Expired or missing artifacts fail closed until the builder is dispatched again; there is no source-build fallback in a credentialed poll. Controller artifacts are executable code trusted to the consumer default branch; changes to that branch's producer workflow remain a protected review concern. Merged [disposable PR #262](https://github.com/kevinmartin/sofa-disposable/pull/262) pinned both callers to Sofa `6d94c85ebb2b94437febdb759399368730b07bcb`. Its secretless [builder run 36810006387](https://github.com/kevinmartin/sofa-disposable/actions/runs/36810006387) uploaded artifact `11138548115` with the exact SHA name and GitHub SHA-256 archive digest; [lifecycle wake 36810077867](https://github.com/kevinmartin/sofa-disposable/actions/runs/36810077867) passed the verification and reconciliation steps without a checkout, setup-go, or build step. Replaying the same wake ID in [run 36810155053](https://github.com/kevinmartin/sofa-disposable/actions/runs/36810155053) also succeeded without a build or additional dispatch. An actual scheduled tick remains to be observed.
- A fresh CodeRabbit full review of the controller path and milestone branch found six actionable gaps. The verified fixes resolve the default branch for scheduled artifact lookup, document the exact trusted build-caller path, route later owner Discovery moves around interrupted-move recovery, retain reserved budgets on recovered pending Discovery tasks, keep transient repair-source reads recoverable while revocations block, and rename two local variables shadowing a Go built-in. The integrated Go suite, vet, staticcheck, module diff, actionlint, and formatting checks passed locally.
- All six review threads received fix-specific replies and are resolved. Sofa code head `cc38feec19fab50a1670e435f34eede585cbe8cf` passed the Go, workflow, and quality-policy checks. Merged [disposable PR #267](https://github.com/kevinmartin/sofa-disposable/pull/267) refreshed the builder and lifecycle pins to that head after actionlint and CodeRabbit passed. Its [secretless build](https://github.com/kevinmartin/sofa-disposable/actions/runs/36812714719) and [manual no-build lifecycle wake](https://github.com/kevinmartin/sofa-disposable/actions/runs/36812789085) both succeeded. An actual scheduled tick is still pending; the PR head created by this report update needs fresh exact-head gate evidence.
- The lifecycle result previously reported holds only in Actions output. Sofa head `5d1bdb0ff0df269387d25cd81628a2ce8aba31c5` adds paired, controller-owned private-Project text fields, `Blocked reason` and `Next action`. It selects bounded fixed text rather than issue/comment/error content, validates both field IDs and types, compares current values, and rechecks the exact issue, Project item, and Status revision before each changed write. It never changes Backlog or Ready. Projects lacking both default fields keep existing behavior; partial or explicitly misconfigured pairs fail setup. GraphQL has no conditional text-field mutation, so an owner move in the final read/write gap remains a documented race. Local full Go tests, vet, staticcheck, module diff, actionlint, and diff checks passed; CodeRabbit's requested review completed on that head without new inline findings. The exact-head [hosted E2E recovery suite](https://github.com/kevinmartin/sofa-disposable/actions/runs/36815423948) completed and posted App-authored `sofa / hosted-e2e` success. Repository-wide `gofmt -l .` still lists the unchanged pre-existing `internal/integrity/bundle_test.go`; all modified Go files are formatted and the hosted quality job passed.
- The delegated disposable Project now has the two private text fields. Merged [disposable PR #271](https://github.com/kevinmartin/sofa-disposable/pull/271) pinned the lifecycle and builder callers to `5d1bdb0`; its [secretless controller build](https://github.com/kevinmartin/sofa-disposable/actions/runs/36815054500) and [manual no-build lifecycle wake](https://github.com/kevinmartin/sofa-disposable/actions/runs/36815123131) passed. A live Project read showed issue #1 in Ready with `approved scope unavailable or changed` and its matching next action, issue #16 in Ready with a distinct `delivery attempt unavailable` reason, and issue #219 still in Spec Review with neither field set. The wake did not dispatch unauthorized Ready work or spend a model turn. An actual scheduled tick is still unobserved.
- A later acceptance audit found that duplicate evidence for an unrelated check could hold Verification even when every required check passed. `GatesPassed` now validates the required plan first and detects ambiguity only among required exact-candidate checks; a new regression case preserves rejection of duplicate required evidence. The full Go suite, vet, staticcheck, module diff, actionlint, and diff checks pass locally. This is a gate-contract correction; live required-gate progression remains unavailable until milestone 04 supplies independent gate evidence.
- Merged [disposable PR #277](https://github.com/kevinmartin/sofa-disposable/pull/277) aligns the lifecycle caller's `schedule` job condition with the reusable workflow: GitHub schedules only the default branch, so the scheduled event need not depend on `github.event.repository.default_branch`; manual dispatch retains its fork and default-branch guard. Actionlint and CodeRabbit passed. The workflow is active on disposable `main`. GitHub has created no new lifecycle `schedule` event since the earlier failed runs, so this removes a possible job skip but does not itself prove cron delivery.
- [Disposable issue #280](https://github.com/kevinmartin/sofa-disposable/issues/280) exercised a material same-issue proposal reset without changing #219 or entering Backlog. The first [Discovery run](https://github.com/kevinmartin/sofa-disposable/actions/runs/36819253395) published [comment 5925294105](https://github.com/kevinmartin/sofa-disposable/issues/280#issuecomment-5925294105), and [manual reconciliation](https://github.com/kevinmartin/sofa-disposable/actions/runs/36819598478) moved it to Spec Review. The issue body then changed the required ordinary and blank outputs to include an exclamation mark, followed by a later delegated Discovery move. The first v2 poll correctly spent no model turn because the trusted consumer cap was one. Reviewed and merged [disposable PR #281](https://github.com/kevinmartin/sofa-disposable/pull/281) raised the aggregate cap to two; the next [poll](https://github.com/kevinmartin/sofa-disposable/actions/runs/36820428252) dispatched a second [Discovery run](https://github.com/kevinmartin/sofa-disposable/actions/runs/36820458701), which published [comment 5925448176](https://github.com/kevinmartin/sofa-disposable/issues/280#issuecomment-5925448176). [Reconciliation](https://github.com/kevinmartin/sofa-disposable/actions/runs/36820873363) returned only the current proposal to Spec Review. The `sofa-state` ledger names the second comment and changed source/spec digests as current, retains the old comment in reset history, records `model_calls: 2` of `max_model_calls: 2`, and has no approved digest or Backlog timestamp. Both comments carry `specification v1` because that marker is the document-format version, not the proposal sequence; the immutable comment ID and digest distinguish proposals. This proves live revision fencing before approval, not owner approval or Ready admission. GitHub still had not emitted a new `schedule` event at the last check.
- Done correction patrols now inspect at most two historical items per poll generation and rotate across them. The recent-default-commit lookup still examines only the latest 100 commits, so a revert outside that window can be missed before an item rotates back; a durable per-item commit cursor is future work. GitHub Projects also offers no conditional status mutation: an owner move between the controller's last read and write can be overwritten. [Project setup](../../lifecycle-setup.md) states this residual race; factory code still cannot target Backlog or Ready.

## Remaining prerequisites and recovery

The disposable consumer now has lifecycle policy, a one-repair limit, and
scheduled/manual lifecycle, Discovery, review, and delivery callers pinned to
reviewed Sofa commits, plus an exact-commit post-merge smoke gate. The #219 specification is waiting in Spec Review for Kevin's
decision. Sofa will not make the Backlog approval move. Project write access
and automations must
remain restricted so untrusted actors cannot set Backlog or Ready. Re-run local
tests and inspect exact Project/issue revisions after code integration. The
designated disposable PR #252 provides successful release-observation evidence;
it does not prove the hosted Discovery, review-repair, or lifecycle handoffs.

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
