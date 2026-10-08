# Sofa toolkit releases

Consumers install the current promoted release through
[setup-cli](../.github/actions/setup-cli/action.yml), usually at `@v0`.
The Action installs Linux amd64 sofa and sofa-test together on PATH. Its one
version selector accepts v0 or v1 major channels, an exact release such as
v0.1.3, or a full lowercase commit SHA; empty defaults to v0. Release selection
verifies the bundle with gh release verify-asset. The version output identifies
the exact release or source SHA installed, and can be supplied as version in a
later job. Each job retains its first selection; a new job may select a newly
promoted compatible release. Reusable v0 workflows pass major: v0 as an internal
guard so their version requests cannot cross to v1.

Exact source selection uses the same Action to check out Sofa without persisted
credentials and build both CLIs locally. Go builds remove GitHub token environment
variables and precede Project, model and publication steps. This explicit
owner-approved candidate path rebuilds independently in each job and needs Go;
released installation needs no Go or binary artifact handoffs. Candidate hosted
E2E remains secretless and selects the matching workflow/source revision. Normal
installation always downloads releases from kevinmartin/sofa.

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
   `release-canary.yml`; that workflow verifies both binary identities
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
Action's empty-selector default to `v1` and change reusable workflows' internal
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
assets, publishes it, and requires `immutable=true`. It never replaces uploaded
assets. GitHub documents that an upstream upload `502` can leave an empty
`starter` placeholder; retries may remove only that zero-byte, digest-free
placeholder after rechecking its exact ID and unchanged reserved draft. Published,
uploaded, nonempty or changed assets fail closed and require inspection. This
uses the existing Contents write permission and still requires release
serialization and exclusive writers because asset deletion has no CAS field.
See [GitHub's upload recovery contract](https://docs.github.com/en/rest/releases/assets#upload-a-release-asset).
It dispatches a real-release canary in the disposable repository and
binds the observed run to its exact caller SHA, workflow path, repository IDs and
per-attempt correlation. The canary recomputes that correlation from the release,
source and originating run/attempt, and verifies the published immutable tag's
source before loading its installer. A failure, cancelled run, changed caller,
ambiguous correlation or unavailable API cannot authorize promotion. Allocation
inspects at most 10,000 release records. Canary discovery uses GitHub's `created`
filter to inspect at most 1,000 runs from the preceding 24 hours, rather than
lifetime history. That window exceeds the release job's maximum lifetime, so
same-attempt observation retries retain their correlation; an Actions rerun has
a new attempt and correlation. Duplicate matches within that window fail closed,
as do missing, out-of-window or future timestamps (with one minute allowed for
clock skew). Canary observation is bounded to ten minutes within the workflow's
job timeout. See [GitHub's workflow run filtering contract](https://docs.github.com/en/rest/actions/workflow-runs#list-workflow-runs-for-a-workflow).

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

## Recover with a forward fix

If validation or the canary fails before promotion, the major channel stays on
the last good release. Correct the failure and retry that release when appropriate,
or merge a reviewed fix to publish a new version. Published assets and exact tags
remain immutable; an older retry cannot overwrite a later promotion.

If a defect is found after promotion:

1. Open a fix PR, or revert the offending PR in a new PR. A revert creates a new
   source commit; do not reuse an older exact release or dispatch an old source.
2. Run normal required quality and hosted checks, then obtain Kevin's merge
   approval. Check compatibility with state written by the defective release.
3. The normal queued release workflow builds the approved main commit, publishes
   a new immutable version, and runs its fresh disposable canary.
4. Only successful qualification advances the major channel to that new release.
   Unchanged consumers receive it on subsequent CLI-using jobs.

For example, a fix or PR revert after v0.1.2 produces v0.1.3. It preserves consumer
configuration, checkpoints, ownership, evidence and counters. Reverting code
does not restore an old ledger or reset budgets; the replacement must still
understand supported persisted state.

Use this same publication process for release recovery. No separate rollback
workflow or additional credentialed channel writer is planned. Existing low-level
rollback planning and downgrade compatibility fixtures remain in the merged CLI
for API compatibility; they are not the factory's recovery procedure.

All credentialed v1 release commands require SOFA_V1_ENABLED=true after the
reviewed activation steps above. Release automation rejects majors beyond v1
before credential use; a future major needs its own reviewed activation path.
