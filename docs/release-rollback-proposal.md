# Serialized toolkit rollback proposal

Status: proposed for Kevin's explicit approval. This document installs no workflow,
moves no ref, and grants no credential. Automatic approval review rejected adding
the credentialed workflow without specific rollback authorization.

The proposed file is .github/workflows/release-rollback.yml. It will be an
owner-dispatched workflow on Sofa main, enabled only after release activation,
with exact release_version and expected_channel_sha inputs. It shares
sofa-toolkit-release, cancel-in-progress: false, and queue: max with normal
release publication. Do not run standalone channel-writing commands concurrently
with either workflow.

## Jobs and credential boundaries

| Job | CLI selection and behavior | Credentials |
| --- | --- | --- |
| prepare | Validate exact v0/v1 semver and full expected SHA; v1 requires its existing activation flag. Install current promoted CLIs with setup-cli, capture current compatibility state, and output the exact controller version | Contents read; no App, model, Project or publisher secret |
| qualify | Install the requested previous exact immutable release through setup-cli. Decode and round-trip the current fixture, including ownership, checkpoints and counters, then compare results | Contents read; no publication secret |
| promote | Depends on both successful jobs. Install the exact controller version output by prepare. Make the guarded rollback plan, require a fresh correlated disposable canary, then recheck the canary and prior channel before updating it | Main-only sofa-release environment; narrowly scoped App tokens below |

Only compatibility state and redacted recovery evidence use same-invocation
artifacts. CLIs download from immutable Sofa releases independently in each job;
there is no renamed controller binary, source build, binary artifact handoff,
release repository override or SHA allowlist.

The final job mints two separate tokens after qualification:

- Sofa token: Contents write, Workflows write and Administration read on Sofa only.
- Canary token: Actions write and Contents read on sofa-disposable only.

The first token is passed only to rollback planning and final promotion; the
second is passed only to canary observation and promotion's evidence recheck.
The App key exists only in the token-mint steps. Earlier compatibility jobs receive
neither token, and no model/provider or Project credential is used anywhere.

The workflow must reject fork/non-main dispatch, invalid selectors, absent v1
activation, unavailable or mutable releases, changed expected channel, incompatible
state, failed/stale/unrelated canary evidence and skipped prerequisites. A failed
check cannot produce promotion. Rollback cannot restore a ledger, reset a budget,
delete evidence, create another factory delivery or merge a PR.

## Validation before enabling this path

Parsed workflow mutation tests will require shared queued serialization, both
qualification dependencies, the main-only release environment, exact controller
version handoff and absence of early secrets. Existing behavioral tests exercise
immutable rollback planning, exact prior-ref rejection, compatibility retention,
failed canaries and stale evidence.

After an owner-approved merge, live qualification needs at least two actual Sofa
releases and the configured release App. It will use a compatible target release
and explicitly observed current SHA, retain the trusted canary/plan, and verify
the major ref while preserving consumer state. No rollback run is authorized by
this proposal alone.

## Approval requested

Approve implementing the workflow above in the post-merge PR. It will remain
unmerged for Kevin's review, and no live rollback will be dispatched without a
separate explicit recovery request.
