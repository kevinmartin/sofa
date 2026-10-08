# Lifecycle Project setup and recovery

Milestone 02 uses one private GitHub Project per consumer. The Project's built-in
`Status` single-select field represents handoffs. Configure the ten names mapped
in the trusted default-branch `.sofa.yml`: Inbox, Discovery, Spec Review,
Backlog, Ready, Building, Verification, Review, Release, and Done. Names may
be changed together with the mapping; canonical stage identities stay fixed.
Keep `ready_status` equal to the Ready mapping.

Create a **Product** view filtered to Inbox, Discovery, Spec Review, and
Backlog, and a **Delivery** view filtered to Ready through Done. Keep an
unfiltered view for audit. Lifecycle decisions keep blocked reason and next
action separate from the stage. Backlog suggestions also appear in the poll
result and Actions run summary. Moving a blocked item to a special column
would hide its handoff.
Create two Project text fields named `Blocked reason` and `Next action` for
item-level advice, and show them in the Product and Delivery views. These are
the default names; configure
`lifecycle.blocked_reason_field` and `lifecycle.next_action_field` together if
the Project uses different names. The controller resolves both field IDs and
checks that they are text fields before reconciliation. Existing Projects
without either default field continue without board advice; a partial pair or
missing explicitly configured field is a setup error. Status remains the
lifecycle position.

Only Kevin, another explicitly designated owner, and the trusted factory
controller credential should have Project write access. GitHub Projects does
not grant option-specific write permissions: the controller credential can
technically set Backlog or Ready, so code and workflow review must enforce
that it never does. Disable or retarget Project
automations that move items into either option. A public issue or comment
cannot authorize Discovery, approval, or implementation. A move into Discovery
is the owner admission gesture; a move from Spec Review to Backlog approves
the exact versioned specification previously displayed for review. Moving a
Backlog item to Ready authorizes delivery only while that approval still
matches. Factory code may move a claimed item to Spec Review or project an
observed delivery outcome, but cannot write Backlog or Ready.

Keep the Project credential in the caller's trusted controller job, never in
the ACP worker, product test, or artifact. The source Project must be private
and repository, Project, issue, item, and option identities must match the
configuration and persisted revision. The controller re-reads the Project
before each mutation and stops when that read shows a conflicting edit.
GitHub's Project field mutation has no conditional update: an owner move in the
gap between that read and the write can still be overwritten without detection.
Restrict Project writers and avoid simultaneous manual and automated moves;
factory code never targets Backlog or Ready. The public API also does not
expose the mover reliably, so Sofa does not claim to identify who made a move.

One scheduled caller should invoke the reusable reconciliation workflow every
ten minutes, or hourly using `lifecycle.poll_minutes: 60`. Manual and event
wakeups use the same ledger and coalesce missed ticks. A poll reads metadata
only; no model is started for unchanged items. After the first promoted v0
release is activated, normal callers use
`kevinmartin/sofa/.github/workflows/lifecycle.reusable.yml@v0` and omit
`version`. Each poll uses the shared setup Action to download both CLIs
from the currently promoted immutable Sofa release, verifies its signed asset,
and retains that selection throughout the job. No Go toolchain, controller-build
dispatch, or expiring controller artifact is needed. Optional `version`
accepts the v0 channel, an exact immutable v0 release, or an explicit full source SHA.
See [release setup and activation](releases.md) before migrating an existing
consumer; keep its legacy caller operational until the v0 channel exists.

Hosted tests of unmerged Sofa source can select a candidate workflow revision
and pass the corresponding full SHA as `version`. The same setup Action
builds both CLIs locally in each job before Project, model or publication secrets
are supplied to later steps.
It is not the normal polling setup. The obsolete `controller-build.reusable.yml`
has been retired; migrate existing callers after the major release channel
is activated. Older immutable workflow revisions remain available for historical
runs; the CLI retains its legacy artifact-reader compatibility.

The poll's bounded output dispatches
the caller-owned Discovery workflow for owner-admitted Discovery items and the
delivery workflow for approved Ready items. The normal Discovery caller uses
`discovery.reusable.yml@v0`, omits `version`, and passes the targeted
`issue_number`; when its trusted publisher uses `github.token` for
issue comments, pin `spec_author_id` to the public node ID of
`github-actions[bot]`. Active Discovery defaults to two
items and code-writing delivery to one per repository; changing either cap is
a trusted configuration change. Child work and repair must reuse the parent's cumulative
budget and PR identity.

For a live owner-review repair, publish draft PRs under an identity distinct
from `owner_id`. GitHub does not let the PR author submit a formal
`CHANGES_REQUESTED` review on that same PR. A consumer can pass
`SOFA_PUBLISH_APP_ID` and `SOFA_PUBLISH_APP_PRIVATE_KEY` to both the delivery
and review reusable callers. The installed App needs **Contents: read/write**
and **Pull requests: read/write** on only that consumer repository. Approve
those permissions on its installation after changing the App, then store its
numeric ID and private key under those two repository secret names. The trusted
publisher job mints a repository-scoped, short-lived installation token and
revokes it at job end. The worker, verifier, and failure finalizer do not see
that key or token. Keep `SOFA_PROJECTS_TOKEN` separate and keep `owner_id` set
to the human reviewer. Existing consumers can continue passing
`SOFA_PUBLISH_TOKEN`; when both App secrets are configured, the App identity
takes precedence so old callers can retain their token during migration. A
partial App configuration fails the publication job rather than falling back
to the legacy identity. Before exercising review repair, verify that the new
draft PR's author ID differs from `owner_id` and that the owner's review binds
its current head.

If `priority_field` is configured, set its Project value to exactly `P0`
through `P4`, with `P0` highest. Within one priority, lower issue numbers run
first. A blank or different value holds that item rather than guessing its
priority. A configured `dependencies_field` uses `none` or comma-separated
same-repository issue references such as `#12, #13`.

If Actions is delayed or cancelled, run the trusted reconciliation caller
again. It must inspect the persisted claim and current Project/PR/check state
before doing work. If a board item was moved manually, inspect its status and
ledger before changing it; do not repeatedly force the expected column. If a
Backlog specification or source changed, the controller holds it and suggests
an owner move to Discovery. A new versioned proposal then returns to Spec
Review and requires a fresh owner Backlog gesture. Earlier bot comments stay
on the issue for audit; review the latest proposal associated with the current
Spec Review move. The `specification v1` comment marker identifies the document
format, not the proposal number. The controller binds the exact current comment
and content digest rather than trusting that visible marker for approval. The
controller never makes either approval move. A missing or stale required gate
leaves the candidate draft and blocked in Verification. After merge, observe
the actual merge/default branch commit and configured release checks before
Done. A failed release remains Release with a linked correction; it does not
grant deployment or rollback authority.

The delegated disposable Project is
[sofa disposable canary](https://github.com/users/kevinmartin/projects/2).
Its ten Status options were installed for milestone 02 while retaining the
existing option IDs. Its private visibility and owner node ID were verified
through GitHub GraphQL. The signed-in Project settings showed no collaborators.
The built-in automations for issue close, item close, PR linked, and PR merged
were disabled because they had moved or closed items outside Sofa's verified
handoffs. New items still enter Inbox, and sub-issues can still be added to the
Project. Audit those settings and Project access again if either changes before
relying on a live Backlog approval.
