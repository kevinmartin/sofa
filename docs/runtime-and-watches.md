# Runtime and watch loops

Planning baseline: September 22, 2026. This document specifies proposed behavior; no runtime or watch integration has been implemented or exercised. It supplements the lifecycle plan with execution tradeoffs, scheduled work, and recovery policy.

## GitHub Actions as the first runtime

Each consuming repository owns its factory, credentials, configuration, state and runs. GitHub Actions supplies event delivery and disposable compute. The Go engine supplies admission, scheduling decisions, budgets, checkpoints, reconciliation and publication. There is no central controller repository or required external database.

Actions is a strong initial fit because work, reviews, checks and execution history already live beside the repository. Shared workflows distribute improvements through the selected sofa release channel. Its limits mean that durable work must live outside a running agent process.

| Property | Benefit or constraint | Sofa response |
| --- | --- | --- |
| Public compute | Standard hosted runner use is free for public repositories. Private repositories have plan allowances; larger runners are charged even for public repositories. | Default standard Linux execution. Budget inference, runner time and storage separately. Free compute does not imply free model use. [Billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions) |
| Disposable environments | Ordinary hosted jobs receive fresh VMs. Current standard Linux capacity differs between public and private repositories: 4 CPU/16 GB RAM versus 2 CPU/8 GB RAM, both with 14 GB SSD. | Pin toolchains and container images; size execution for the private-repository profile. Treat local files and process sessions as disposable. [Runner resources](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) |
| Bounded execution | Hosted jobs generally stop after six hours; account concurrency is finite. | Keep shorter sofa limits, initially 45 minutes per attempt and one code-writing attempt per repository. Split long work at durable phase boundaries. [Actions limits](https://docs.github.com/en/actions/reference/limits) |
| Best-effort scheduling | Scheduled runs can be delayed or dropped, execute from the default branch, and can be disabled after 60 inactive days in public repositories. | Offset schedules from the hour, reconcile overdue work, and provide event/manual wakeups. Do not promise exact execution times. [Schedule behavior](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule) |
| Finite evidence retention | Logs and artifacts normally expire after 90 days. GitHub documents changes to checks/run/status retention beginning October 1, 2026. | Preserve compact accepted outcome summaries and provenance before evidence expires. Artifacts transport results; they are not the lifecycle database. [Retention policy](https://docs.github.com/en/organizations/managing-organization-settings/configuring-the-retention-period-for-github-actions-artifacts-and-logs-in-your-organization) |
| Temporary credentials | Hosted `GITHUB_TOKEN` ends with the job or its effective maximum lifetime. | Obtain credentials independently for each trusted job. Never persist tokens in checkpoints, caches or memory. [Token lifecycle](https://docs.github.com/en/actions/concepts/security/github_token) |
| Cold starts and specialized resources | Agent installation, browser setup and model loading add startup work. GPU and private-network requirements need suitable runtime profiles. | Publish pinned prepared images; cache verified dependencies; benchmark local System One execution. Paid resources require explicit configuration. [Runner options](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) |
| Cache and artifact trust | Cached files and results can originate in untrusted execution. Accessible caches may be readable by fork workflows. | Never cache credentials, private memory, authorization state or trusted executable policy. Privileged publication validates provenance and does not execute retrieved results. [Cache security](https://docs.github.com/en/actions/concepts/workflows-and-actions/dependency-caching) |

Reconciliation should be cheap enough to run without an agent or repository build. Preserve the ten-minute polling preset and provide an economy preset with hourly polling plus manual/event wakeups. Ten-minute polling produces 4,320 ticks over 30 days before useful work. Private repositories therefore need a runner budget even when every idle tick uses zero inference. Use one repository ticker to evaluate all due watches rather than a separate scheduled workflow per watch.

The published budget covers admitted work and retries across runs. Exhaustion defers discretionary work; it does not select a paid provider or larger runner automatically. Track queue delay, setup time, useful execution time and provider spend separately so optimizations target actual cost.

## What to borrow from Warren

Warren's named watches are useful examples of separating discovery, diagnosis and maintenance. Its current operating experience also argues for restraint. The configuration inspected directly on September 22, 2026, at commit `ae4028d3d5ba51defa266aa9555349e93bf564e5` disables all schedules. Its comments say the gatewatch, ratchetwatch, tastewatch and warden-digest population was retired because token-consuming audits generated findings faster than they were triaged; mechanical gates already enforced relevant standards. Nightwatch and bugwatch remain built-ins but are not scheduled in that configuration. This is a pinned source snapshot, not an assertion based on an older cached README. [Warren trigger configuration](https://github.com/jayminwest/warren/blob/ae4028d3d5ba51defa266aa9555349e93bf564e5/.warren/triggers.yaml)

Sofa should borrow the separation of responsibilities and measure each watch's usefulness. A deterministic gate already catching a violation does not need a nightly LLM to rediscover it. Generative patrols need fresh evidence, a clear question, a proposal cap and someone able to act on their output.

## Observation, admission and activation

Every loop begins with deterministic observation. Go collects structured signals, reconciles identifiers and revisions, checks due times, deduplicates findings and applies trusted policy. It activates System One or System Two only when an unresolved judgment or concrete permitted task remains. An enabled watch is not an instruction to spend tokens every time the ticker wakes.

There are two implementation admission paths:

1. **Ready admission:** Kevin's authorized Ready transition approves the scoped issue for implementation. Discovery first produces a draft specification; Kevin approves that specification before the card enters Backlog. Ready remains a separate authorization to build it.
2. **Watch-policy admission:** a configured watch may open bounded fix PRs without a Ready transition. Its standing grant lives in trusted default-branch configuration and specifies the watch, eligible change classes, allowed paths, required validation and resource limits. The ledger records that grant and its revision as the authority. It must not invent a human board transition.

Automatic fixes are optional per watch. Enable them only for enumerated repairs such as a known formatter recipe, a reproducible narrow regression, or an approved dependency-update class. Broad redesigns, unclear causes and changes outside the grant become Discovery proposals. Their draft specifications require Kevin's approval before Backlog. Kevin merges all resulting PRs; watch grants do not authorize deployment, gate weakening or modification of sofa's own authority rules.

Before publication, revalidate the current grant, source revision, expected branch head and active attempt ownership. Revoked or changed authority pauses publication. Findings and fixes stay within the calling repository; shared-toolkit improvements become local proposals for Kevin to promote into sofa, unless a separate future cross-repository capability is explicitly configured.

## Proposed catalogue

Cadences below are presets evaluated by the repository ticker. Onboarding enables applicable watches and their specific grants; it does not enable every generative scan automatically.

| Loop | Trigger or cadence | Work and activation | Automatic fix boundary |
| --- | --- | --- | --- |
| Reconcile | Every tick; manual/event wakeups | Recover attempts, synchronize state, dispatch admitted work. No inference. | Existing admission only. |
| Gatewatch | Failed required check; poll fallback | Parse structured failures, apply known recovery, classify unfamiliar evidence and route diagnosis. | Same admitted PR or an explicit narrow gate-repair grant. |
| Nightwatch | Freshness checks every tick; nightly scoped probes | Inspect elapsed deadlines and missing expected observations even with unchanged code; run configured smoke tests and investigate deployment drift or unexplained failures. | Only configured reproducible repairs; other findings enter Discovery. |
| Bugwatch | Eligible bug events; daily sweep | Skip duplicates, blocked work and active plans; reproduce and scope remaining bugs. | Configured small bug-fix classes; otherwise draft specification. |
| Security/dependency watch | Applicable PR checks; daily advisory refresh; optional weekly deeper assessment | Process scanner output first. Investigate credible unexplained findings. | Configured dependency/security repairs with mandatory validation; sensitive findings use the repository's private reporting destination. |
| Tastewatch | Changed-file review during delivery; optional weekly sample | Mechanical formatting/lint first; fresh-session judgment for remaining consistency or readability questions. | Approved deterministic style recipes; subjective refactoring remains a proposal. |
| Ratchetwatch | Measure after merge; aggregate weekly | Identify supported tightening of quality baselines and rules. | Bounded test/measurement PRs when granted; protected gate-threshold changes become patch proposals for owner handling. |
| Dreaming | Weekly with new work outcomes or later feedback/corrections | Aggregate outcomes and repeated procedures; propose memory, skill, recipe or routing improvements. | Configured evidence-backed proposal PRs; protected configuration changes become patch proposals for owner handling. |

Deployment probes may remain useful when source has not changed, because external dependencies and deployed behavior can change independently. Such probes must have explicit targets and safe operations. Agent exploration is separate from required deterministic validation; its findings should become reproducible checks where possible. Dreaming/evaluation housekeeping alone never qualifies as fresh work for another dreaming run.

Sensitive findings in public repositories require an explicitly configured private reporting destination. Do not put raw details in public issues, logs, artifacts, or state; only an approved redacted summary may enter public Discovery. If that destination is missing, block sensitive publication and request private handling without disclosing the finding.

Default limits are three new Discovery findings per watch execution, one open improvement PR per ratchet/dreaming watch, and one active code-writing attempt across the repository. Reuse the factory's two repair attempts and two transient infrastructure retries, persisted across runs. A watch's own unresolved-finding cap pauses further discretionary investigation until its backlog shrinks. Required checks and deterministic observation continue.

Foreground admitted work and broken required gates outrank optional maintenance. Each enabled watch also requires a finite execution/model budget. Its output records evidence, scope and disposition; a clean observation creates no issue or comment. Track accepted findings, duplicates, obsolete findings, successful repairs and cost per accepted result. Disable or narrow watches that consistently create noise.

## Failure routing and durable recovery

Go handles known runner interruptions, cancellations, provider throttling and missing prerequisites using explicit policy. Unknown failures may go to Jev with bounded candidate causes and an unknown option. A classification selects the next investigation; it is not proof of root cause. Authentication failures block, quota exhaustion defers, and diagnosed regressions go to a permitted fixer. Neither a classifier nor a fixer can ignore a failed gate or reset the retry budget.

All triggers enter the same ledger-backed queue. Record loop/version, evidence revision, scheduled window, input digest, parent attempt, admission kind, authority revision, ownership generation, checkpoints and counters. Event deduplication includes the relevant issue or exact commit/check revision. Scheduled work also records the covered observation window; after downtime, coalesce overdue patrol windows into one current evaluation.

Persist an attempt before dispatch. Publish bounded checkpoint artifacts at completed phase boundaries and record their provenance before treating them as recoverable. A worker killed before checkpoint publication may lose local progress. Recovery restarts from the latest accepted checkpoint and rechecks real GitHub state, avoiding duplicate PRs or replacement of human changes. An expired artifact invalidates that recovery path; it never implies success.

GitHub concurrency is a capacity control, not the authoritative queue. Its current optional `queue: max` permits up to 100 pending entries; the ledger still decides admission, priority and retry eligibility. [Concurrency behavior](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)

Use an App token for publication when unattended downstream PR CI is needed. `GITHUB_TOKEN` suppresses most recursive events, while certain automated PR events now enter an approval-required state. A `workflow_run` wakeup can have elevated credentials, so it must verify provenance and only invoke trusted control-plane logic. [Workflow triggering](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow), [workflow_run security](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run)

An Actions-only factory cannot reliably detect or alert on its own complete outage or disabled schedule while nothing runs. Expose last-success timestamps and recover at the next invocation. An optional later independent heartbeat may alert or dispatch a repository reconciliation; it need not own task scheduling or become a central controller.

Keep dispatch, observation, cancellation and result retrieval behind a small Go runtime interface. The initial adapter uses Actions; loop policy, ACP sessions, recipes and memory remain independent of runner-specific environment variables. A future local/container runtime can reuse those contracts. The guarantee is recoverable work with bounded repeated effort, rather than an immortal agent session.
