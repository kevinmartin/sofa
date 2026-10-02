# Shared quality and managed repository files

`quality.reusable.yml` is sofa's secretless PR quality gate. Sofa calls it from
`pr-fast.yml` by local path, so a PR exercises its proposed version of the
workflow. The trusted default-branch bridge still listens for the unchanged
`Sofa / PR deterministic checks` workflow name and dispatches the separate
hosted E2E gate. The bridge reads the candidate quality workflows **as data**
and rejects changes that remove the minimum validators or broaden permissions.
It never executes the candidate with its App credential.
The trusted bridge compares the candidate caller and reusable workflow with
their exact main-branch base revisions. The caller must be unchanged. In the
reusable workflow, only a full 40-character action commit SHA and its version
comment may change on an existing action line; action names, commands,
permissions, conditions, and other bytes must match the trusted base. There
is no approved-SHA list or release lookup. The candidate still runs every
quality job. A full SHA is immutable but this policy does not attest that it
belongs to an official release; candidate quality jobs are secretless and
read-only. Other workflow changes remain blocked until a separately merged,
Kevin-reviewed bridge policy update deliberately admits them. The hosted E2E
observer applies the same comparison to its candidate workflow. A changed candidate
workflow needs an up-to-date main base for that comparison.

`.github/CODEOWNERS` requests Kevin for workflow and gate-policy changes.
Sofa's personal-repository branch rule cannot use a required owner review for
PRs authored by the same account, so Kevin still controls the final merge of
those PRs. Require `sofa / quality-policy` from the sofa App in branch
protection; that automated status checks the trusted boundary but does not
approve a dependency upgrade's compatibility or security impact.
The bridge publishes `sofa / quality-policy` from the sofa-scoped App on the
current PR head. Branch protection requires this status and `sofa / hosted-e2e`
from App `5077388`, plus `quality / result` from the GitHub Actions App
`15368`, with strict up-to-date checks and administrator enforcement. The
policy status verifies the workflow contract; `quality / result` proves the
actual validators passed. A scheduled disposable fallback cannot clear a failed
policy status. The old `deterministic` job has been retired; the caller now has
one job invoking the reusable quality workflow.

The `profiles` workflow-call input accepts an empty value (the default),
`auto`, or a comma-separated list of `go`, `typescript`, and `react`. The
`autodetect` job runs only for empty/`auto` input; `select` validates the
result or the explicit profiles. Go and TypeScript/React checks then run in
parallel when selected. GitHub Actions lint runs independently for every
caller. The always-running `quality / result` job requires every selected
job and Actions lint to succeed, including when a job is skipped unexpectedly.
Auto detects `go.mod`,
`package.json` plus `tsconfig.json`, and React in package dependencies. It fails
when no profile applies. Explicit profiles fail if their manifests are absent.
TypeScript requires exactly one npm, pnpm, or Yarn lockfile; pinned
`packageManager` is required for pnpm and Yarn. Package scripts
`format:check`, `lint`, `typecheck`, and `test` are required. React also requires
`build`. Node installs ignore lifecycle scripts; test/build scripts still run
from the untrusted PR checkout without secrets. Repositories with private
packages need a separate reviewed design before using this gate.

After the first tested release, downstream callers can reference
`kevinmartin/sofa/.github/workflows/quality.reusable.yml@v0`. The moving major
reference gets reviewed compatible fixes on subsequent runs. The consumer
template and its structural validator use this initial channel; Sofa itself
keeps its local call so PRs test proposed quality changes. Initial activation
follows Kevin's implementation merge and the release setup in
[release distribution](releases.md). Stable `v1` remains an explicit owner
activation after integrated qualification. A rerun of all jobs can resolve a newer moving ref;
the run records the resolved workflow revision for audit. Consumer caller
files change through PRs, even though compatible called-workflow updates take
effect without editing those files.

## Repository configuration

An enrolled repository has a reviewed manifest under `managed-repos/` named
`owner--repository.yaml`. It selects only predefined quality profiles,
Dependabot ecosystems, a schedule, and an open-PR limit. Project tickets may
request enrollment or a change, but their text cannot select an arbitrary
repository or inject workflow YAML. The first version manages only a small
quality caller (`.github/workflows/sofa.quality.yml` for consumers and
`pr-fast.yml` for sofa) and `.github/dependabot.yml`. New repositories must already
exist; creation and GitHub settings are later work.

From a trusted sofa checkout:

```sh
go run ./cmd/sofa config --repo kevinmartin/sofa check
go run ./cmd/sofa config --repo kevinmartin/sofa render
go run ./cmd/sofa config --repo OWNER/REPO reconcile
```

`check` proves that sofa dogfoods its own rendered configuration. `render`
prints desired path/content pairs for review. The caller and Dependabot files
come from embedded templates; the renderer parses both YAML documents and
checks their required structure against the manifest. `render` and `check`
require `actionlint` on `PATH` and lint the generated caller. `reconcile
--apply` repeats that lint before any GitHub request. Sofa's Actions job lints
both self and consumer template variants on every PR. `reconcile` is read-only by
default and reports remote drift. To open or update one PR on the dedicated
`sofa/config` branch, set `SOFA_CONFIG_TOKEN` to a short-lived GitHub App
installation token restricted to the enrolled repository and run
`reconcile --apply`. The App needs repository Contents and Pull requests write
permissions, and permission to update workflow files. Keep the token in a
trusted default-branch environment, never in a PR job. The reconciler refuses
to replace a file that lacks its management marker, refuses unrelated changes
or a branch behind the default branch, and never merges its PR. When the
default branch already has the desired files, an apply run closes any stale
config PR; a read-only run reports it without changing it. The reconciler uses
sofa's shared bounded GitHub API client. Public read-only plans may use an
anonymous client; writes require the configured token.

Sofa's own `main` still requires Kevin's review and merge approval. The
existing `reconcile.reusable.yml` and `work.reusable.yml` are not yet invoked
by sofa itself. Once those loops are admitted, they can call this deterministic
configuration command after the normal specification and Ready gates.
