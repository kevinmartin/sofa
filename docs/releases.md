# Sofa toolkit releases

Consumers install the current promoted release through
[setup-cli](../.github/actions/setup-cli/action.yml), usually at `@v0`.
The Action installs Linux amd64 `sofa` and `sofa-test` together on `PATH`, verifies
the bundle with `gh release verify-asset`, and outputs its exact `release_version`.
An explicit exact version must stay within the requested major. Install once in a
job; subsequent steps retain that job's version. Dependent jobs can pass the output
back as `release_version` when they need the same version, while omitted inputs
adopt the latest promoted compatible release at each job's start.

Candidate hosted E2E keeps the separate secretless exact `toolkit_sha` source
build. It never requires releases in the disposable repository. Normal installation
always downloads releases from `kevinmartin/sofa`.

## Owner-controlled activation

The release workflow is disabled until `SOFA_RELEASE_ENABLED` is exactly `true`.
Do not set this variable before Kevin approves the implementation merge and these
prerequisites are configured:

1. Enable release immutability in Sofa Settings → General → Releases. Existing
   mutable releases do not become immutable retroactively. There are no existing
   toolkit releases to migrate at the start of milestone 02.1.
2. Create the `sofa-release` environment, restricted to `main`. Configure its
   `SOFA_GATE_APP_ID` and `SOFA_GATE_APP_PRIVATE_KEY` references for the installed
   App. Never put private-key values in files or issue comments. The environment's
   approval policy remains owner-controlled.
3. The App needs Sofa **Contents: write**, **Workflows: write**, and
   **Administration: read**. Administration read performs the immutable-release
   preflight; the controller never changes repository settings. Workflows write
   permits exact-source tags even if later main changes altered workflow files.
   In `sofa-disposable`, it needs **Actions: write** and **Contents: read**.
   Tokens are minted separately for Sofa and the disposable repository, each
   limited to its requested permissions. Existing gate tokens remain narrowly
   scoped regardless of the App's installed permission ceiling.
4. Install the trusted `.github/workflows/sofa-release-canary.yml` caller on the
   disposable default branch. It declares string inputs `release_version`,
   `source_sha`, `correlation`, `release_run_id`, and `release_run_attempt`, and uses
   `run-name: Sofa release canary / ${{ inputs.correlation }}`. It calls
   `release-canary.reusable.yml`; that workflow verifies both binary identities
   and round-trips the configuration/state compatibility fixture without Project,
   publisher or model credentials.
5. Protect exact semantic-version tags from update/deletion. Restrict creation and
   updates of moving major tags to this release App. Do not create releases or
   branches named `v0` or `v1`. Include these ref restrictions in repository
   rulesets, with a narrowly scoped bypass for the release automation where
   needed; other workflows/users must not write moving channel refs.
6. After the approved merge, enable `SOFA_RELEASE_ENABLED` and dispatch
   `Sofa / toolkit release` on `main` to activate v0. Leave `SOFA_V1_ENABLED`
   unset until Kevin explicitly authorizes stable v1 after qualification.

Before stable v1 activation, a reviewed major API migration must change the setup
Action's default expected major to `v1` and change reusable workflows' internal
setup Action references and explicit `major` values from `v0` to `v1`. Qualify that
the resulting `@v1` Action and workflows install v1, while the retained `@v0`
revision continues to select v0. Changing only the release series or enabling
`SOFA_V1_ENABLED` does not perform that migration. These changes and final stable
activation remain milestone 10 owner decisions.

The reviewed [.github/sofa-release.json](../.github/sofa-release.json) starts with
series `0.1`; every validated main release allocates its next patch. Minor/major
series changes require a reviewed configuration change. v1 allocation and channel
promotion also require the separate `SOFA_V1_ENABLED=true` activation variable.

GitHub documents the [immutable-release setting and its administration-read
permission](https://docs.github.com/en/rest/repos/repos#check-if-immutable-releases-are-enabled-for-a-repository),
[draft publication and immutable release identity](https://docs.github.com/en/rest/releases/releases),
and [signed asset verification](https://cli.github.com/manual/gh_release_verify-asset).

## Release stages and recovery

`release.yml` runs Sofa's own reusable quality workflow on the exact approved main
commit. A scoped allocator job selects or resumes its version; a separate
secretless preparation job builds both CLIs with embedded version/source identity.
Their deterministic bundle and
`release.json` metadata are transferred only within that trusted workflow run.

The allocator performs only reads, but uses a Sofa-scoped push-capable token:
GitHub hides draft releases from identities without push access. This lets retries
recognize unfinished releases. Binary preparation runs in its separate secretless
job after allocation.

The credentialed controller creates a draft exact-version release, uploads all
assets, publishes it, and requires `immutable=true`. It never changes an existing
asset; a conflicting or interrupted upload remains a failed draft requiring
inspection. It dispatches a real-release canary in the disposable repository and
binds the observed run to its exact caller SHA, workflow path, repository IDs and
per-attempt correlation. The canary recomputes that correlation from the release,
source and originating run/attempt, and verifies the published immutable tag's
source before loading its installer. A failure, cancelled run, changed caller,
ambiguous correlation or unavailable API cannot authorize promotion. Allocation
inspects at most 10,000 release records, canary discovery at most 1,000 runs, and
canary observation is bounded to ten minutes within the workflow's job timeout.

The entire workflow is serialized, with GitHub's `queue: max` preserving up to
100 pending releases instead of replacing a single pending run. If that bound
is exceeded or GitHub otherwise cancels a release, rerun its original Actions run
to retain the exact approved source; a fresh manual dispatch releases current main.
It records the expected prior channel SHA in
the release's immutable body and rechecks that ref immediately before updating it
directly. GitHub's refs API has no atomic compare-and-swap field; the exclusive
writer ruleset and workflow serialization are therefore required. Never delete
the major tag before updating it. A competing prior-ref change fails rather than
overwriting another promotion. An already completed identical promotion is
idempotent.

Rerun a failed job or dispatch on the same source to resume its existing version.
The controller recognizes drafts and published releases by exact source identity,
compares existing asset bytes, and keeps the originally reserved prior channel
guard. A later promotion cannot turn an older retry into an implicit rollback.
No stage restores a ledger, resets counters, or edits consumer durable state.

An intentional rollback is a separate owner operation: download and verify the
desired previous exact release, run its `sofa-test distribution
compatibility-fixture --input` against the supported current fixture, run its
fresh disposable canary, and update the major tag with the observed current SHA
as the explicit expected prior ref. Preserve all consumer configuration,
checkpoints, ownership, evidence and counters. A failed compatibility check or
changed prior ref blocks the operation. Full live production rollback evidence
follows activation; deterministic premerge fixtures are recorded separately.

After those checks, create a read-only rollback plan with
`sofa release rollback-plan --release-version v0.1.0 --expected-channel-sha "$CURRENT_SHA" > release-plan.json`.
Run `sofa release canary` to obtain fresh correlated evidence, then
`sofa release promote` with that plan and evidence. Supply repository-scoped
credentials only to the commands that need them; do not execute state fixtures
with publication credentials. `rollback-plan` never updates a tag or edits a ledger.
