# sofa

`sofa` is Kevin's GitHub Actions software factory toolkit. Reusable workflows
live here; each consumer repository owns its issue/Project source, execution,
state, and credentials. The `v0` channel selects the latest promoted,
immutable Sofa release within major version 0 for each CLI-using job. It takes
effect only after Kevin merges the distribution change and the first release
passes its disposable canary. No `v0` channel is active before that promotion;
`v1` remains owner-controlled after release qualification.

The initial delivery path is draft-only: a reviewed issue in a
private Project's `Ready` status can produce one bounded candidate through
Copilot ACP. Sofa freezes the issue specification internally at admission and
rejects later edits. A separate secretless job
checks that candidate, then a trusted job revalidates authority and opens or
finds one draft PR. The run does not merge, mark ready, or deploy code.

Start with the [distribution contract](docs/milestones/02.1-versioned-distribution.md),
[disposable consumer setup](examples/consumer/README.md),
[configuration schema and editor setup](docs/configuration.md), and
[current evidence](docs/milestones/evidence/01-delivery-slice.md). The broader
architecture and future milestones are in [PLAN.md](PLAN.md) and
[the milestone index](docs/milestones/README.md).

Coding agents should start with [AGENTS.md](AGENTS.md), which links the shared
conventions, validation commands, and task-specific development skills.

Sofa's shared PR quality gate and its reviewable repository-file reconciler
are described in [shared quality and managed configuration](docs/quality-and-config.md).

After `v0` activation, normal consumers call the reusable workflows at `@v0`
without `version`; each job downloads both Linux amd64 CLIs from the
currently promoted release. Direct Action consumers can use
`kevinmartin/sofa/.github/actions/setup-cli@v0` to put `sofa` and `sofa-test`
on `PATH`. The Action exposes `version` for an optional exact-version
handoff between jobs and `bin_directory` for steps that need a filesystem path.
Released installation needs no Go or controller-build dispatch in the consumer. The
downloaded bundle must pass GitHub's signed release-asset verification before
either CLI is installed. Hosted tests of an unmerged candidate instead select
its exact full source SHA through the same setup Action's source-build path.

This public repository holds **no Kevin-owned reusable workflow secret**.
Callers pass their own Project read credential explicitly; the scoped
`GITHUB_TOKEN` created for a caller run handles repository state and Copilot
Requests in distinct jobs. A separate repository-scoped publisher credential
updates the publication ledger and opens the draft PR only after trusted
verification. Invoking the public workflow
from another repository cannot inherit Kevin's consumer secrets. The agent
receives only its Copilot token inside a disposable container, and its file
changes become publishable only through the path and evidence validator.
