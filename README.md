# sofa

`sofa` is Kevin's GitHub Actions software factory toolkit. Reusable workflows
live here; each consumer repository owns its issue/Project source, execution,
state, and credentials. The first canary pins one immutable sofa commit while
the reusable contract is proven. The planned stable channel will let consumers
receive reviewed toolkit updates automatically after release qualification.

The current milestone is a draft-only delivery slice: a reviewed issue in a
private Project's `Ready` status can produce one bounded candidate through
Copilot ACP. Sofa freezes the issue specification internally at admission and
rejects later edits. A separate secretless job
checks that candidate, then a trusted job revalidates authority and opens or
finds one draft PR. The run does not merge, mark ready, or deploy code.

Start with the [milestone plan](docs/milestones/01-delivery-slice.md),
[disposable consumer setup](examples/consumer/README.md), and
[current evidence](docs/milestones/evidence/01-delivery-slice.md). The broader
architecture and future milestones are in [PLAN.md](PLAN.md) and
[the milestone index](docs/milestones/README.md).

This public repository holds **no Kevin-owned reusable workflow secret**.
Callers pass their own Project read credential explicitly; the scoped
`GITHUB_TOKEN` created for a caller run handles repository state and Copilot
Requests in distinct jobs. A separate repository-scoped publisher credential
updates the publication ledger and opens the draft PR only after trusted
verification. Invoking the public workflow
from another repository cannot inherit Kevin's consumer secrets. The agent
receives only its Copilot token inside a disposable container, and its file
changes become publishable only through the path and evidence validator.
