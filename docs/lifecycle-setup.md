# Lifecycle Project setup and recovery

Milestone 02 uses one private GitHub Project per consumer. The Project's built-in
`Status` single-select field represents handoffs. Configure the ten names mapped
in the trusted default-branch `.sofa.yml`: Inbox, Discovery, Spec Review,
Backlog, Ready, Building, Verification, Review, Release, and Done. Names may
be changed together with the mapping; canonical stage identities stay fixed.
Keep `ready_status` equal to the Ready mapping.

Create a **Product** view filtered to Inbox, Discovery, Spec Review, and
Backlog, and a **Delivery** view filtered to Ready through Done. Keep an
unfiltered view for audit. Blocked reason and next action are separate ledger
metadata; moving a blocked item to a special column would hide its handoff.

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
before each mutation and stops on a conflicting human edit. GitHub's public
Project field API does not expose the mover reliably, so this design relies on
restricted Project write access rather than claiming to know which human made
each move.

One scheduled caller should invoke the reusable reconciliation workflow every
ten minutes, or hourly using `lifecycle.poll_minutes: 60`. Manual and event
wakeups use the same ledger and coalesce missed ticks. A poll reads metadata
only; no model is started for unchanged items. Its bounded output dispatches
the caller-owned Discovery workflow for owner-admitted Discovery items and the
delivery workflow for approved Ready items. The Discovery caller passes an
exact `toolkit_sha` and targeted `issue_number` to
`discovery.reusable.yml`; when its trusted publisher uses `github.token` for
issue comments, pin `spec_author_id` to the public node ID of
`github-actions[bot]`. Active Discovery defaults to two
items and code-writing delivery to one per repository; changing either cap is
a trusted configuration change. Child work and repair must reuse the parent's cumulative
budget and PR identity.

If `priority_field` is configured, set its Project value to exactly `P0`
through `P4`, with `P0` highest. Within one priority, lower issue numbers run
first. A blank or different value holds that item rather than guessing its
priority. A configured `dependencies_field` uses `none` or comma-separated
same-repository issue references such as `#12, #13`.

If Actions is delayed or cancelled, run the trusted reconciliation caller
again. It must inspect the persisted claim and current Project/PR/check state
before doing work. If a board item was moved manually, inspect its status and
ledger before changing it; do not repeatedly force the expected column. If a
specification changed, return it to Spec Review and require a fresh owner
Backlog gesture. A missing or stale required gate leaves the candidate draft
and blocked in Verification. After merge, observe the actual merge/default
branch commit and configured release checks before Done. A failed release
remains Release with a linked correction; it does not grant deployment or
rollback authority.

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
