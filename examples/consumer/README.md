# Disposable sofa consumer

This directory is a template for the owner-designated disposable repository. The
sample test is intentionally red until the approved issue makes the greeting
trim names and use `Hello, friend` for a blank name. The exact path allowlist
excludes the test, so a candidate cannot pass by editing its assertion.

1. Copy the files into the disposable repository. Rename
   `.github/workflows/sofa.yml.example` to `.github/workflows/sofa.yml`.
2. Publish a reviewed sofa toolkit commit. Replace all three forty-zero values
   in the caller with its **same full commit SHA**. The two reusable workflow
   references and `toolkit_sha` must refer to that commit.
3. The example already names the designated `kevinmartin/sofa-disposable`
   repository and Kevin's immutable ID. Replace only `project_id` in
   `.sofa.yml` with the disposable Project's immutable node ID. Keep the
   repository's default branch protected from unreviewed workflow edits. The
   Project needs a status named `Ready`.
4. Set repository secret `SOFA_PROJECTS_TOKEN` to a credential that can read
   that issue, its approval comment, and its Project status history. Set
   `SOFA_PUBLISH_TOKEN` to a separate fine-grained PAT scoped only to this
   repository, with Contents and Pull requests write
   permissions. Only the trusted publication job receives it. The workflow's
   `GITHUB_TOKEN` writes the `sofa-state` branch through its explicit job-level
   Contents permission; the repository default may remain read-only. The
   repository-wide setting that also permits Actions to approve PR reviews is
   not needed. Copilot Requests permission and a Copilot-enabled account are
   needed for the agent job.
5. Create one issue titled **Normalize greeting names**, copying
   [issue-body.md](issue-body.md) as its exact body. Build the toolkit CLI and
   run `sofa spec-digest --title 'Normalize greeting names'
   --body-file issue-body.md`. Kevin must post `/sofa approve-spec <digest>`
   himself, then move that issue into `Ready` in the Project. Run **sofa
   disposable canary** manually on the default branch with that issue number.
   For the untouched example body, the digest is
   `cf358e96c2ddc9e03d6b6f7ff2025637507f508617642267e8d9467213205673`;
   recompute it if GitHub or the issue creator changes any substantive text.

The workflow admits the issue before the model job. An idle or duplicate run
completes without dispatching work or consuming inference. The model receives
only a temporary token with repository read and Copilot Requests permissions.
The sample allows one ten-minute agent attempt, one ACP turn, no repair turn,
and one infrastructure retry. Copilot's internal model-call count remains
unknown when ACP does not report it.
The candidate is checked in a separate job with no model, Projects, state, or
publication secret in the test process. The final job validates the artifacts,
live admission, and state fence before opening or finding one draft PR. This
milestone does not mark PRs ready or merge them.

To test the exact formatter recipe, use a separate approved issue whose body
contains `<!-- sofa:recipe=gofmt -->` and make `fixture/greeting.go` unformatted
on its admitted base revision. The configured recipe path is exact and runs
without Copilot. A missing path or already formatted file does not count as a
successful recipe candidate.

For recovery, rerun the same issue after the earlier Actions run is terminal.
The ledger either suppresses duplicate work or reclaims the attempt. If a
publication intent survived, the verifier fetches the previous run's candidate
artifact and checks it again under the new fence. Artifacts have a one-day
retention; if the needed artifact has expired, recovery stops rather than
inventing evidence. Do not delete the `sofa-state` branch to retry. This slice
uses its own deterministic check evidence and leaves independent downstream CI
qualification to later milestones.

The worker image, Copilot CLI tarball and external actions are pinned. Copilot
CLI v1.0.86's Linux x64 tarball is checked against its published SHA-256 before
execution. The worker runs as a non-root UID in a read-only container with only
the disposable checkout, manifest, candidate output, trusted binary and
Copilot executable mounted. The CLI's ACP file callbacks use the configured
allowlist; native harness tools can bypass those callbacks. The separate
publisher validates every candidate path and file before writing. This
prototype does not enforce a destination-specific network egress policy inside
the container, so the Copilot credential remains sensitive to the harness.
