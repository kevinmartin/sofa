# 03 — Complete the ACP and model-endpoint matrix

## Outcome

The same admitted task contract can use Copilot, Codex, or Claude through Caelis-backed ACP adapters, with explicitly selected model endpoints and isolated authentication. A compatibility report states which exact combinations were actually tested.

Requires [01](01-delivery-slice.md). Read its adapter/evidence handoff, [the shared contract](README.md), and [PLAN.md](../../PLAN.md), especially ACP, endpoint, and credential boundaries. This goal can develop independently of 02/04 once the shared contracts are agreed.

## Prerequisites

Replay and transport tests need no subscriptions. Live checks need configured native Copilot authentication, a Codex API/Responses profile, a Claude profile, an OpenRouter key with an approved finite budget, and a user-provided FreeLLMAPI endpoint where that target is to be validated. Use only permitted data and disposable fixtures. Missing credentials do not justify forwarding another profile's token or silently purchasing access.

## Scope

- Keep Caelis protocol types private. Add/configure native Copilot ACP, the Codex ACP adapter, and the Claude ACP adapter with pinned compatible harness versions. Implement capabilities, streaming, permission requests, multi-turn behavior, deadlines, cancellation, and process cleanup consistently.
- Implement explicit per-role profiles: harness, endpoint/provider, native wire protocol, model, optional supported reasoning effort, auth source, and limits. Discovery, implementation, review, blackbox, security, diagnosis, supervision, and dreaming may share a profile while using isolated sessions and permissions.
- Add direct protocol-specific endpoint presets. Copilot targets Chat Completions; Codex targets Responses; Claude targets Anthropic Messages. Custom hostnames must satisfy the actual protocol and configured data-routing policy. Do not introduce OpenCode as an intermediary.
- Implement OpenRouter presets, including a free-only pool where compatible, and connection to the user's FreeLLMAPI instance for the Copilot/Codex targets in the plan. Claude plus non-Claude/free-pool backends remains explicitly experimental; it is not a substitute for the required Claude-compatible route.
- Isolate auth/config/home state per profile. Native subscription profiles reject endpoint overrides. Router profiles use explicit endpoint credentials and clear incompatible native login state. Credential-bearing endpoints require HTTPS with normal certificate validation; reject HTTP before attaching credentials and reject redirects rather than forwarding credentials to another origin. Cover these boundaries in adapter tests. Unsupported combinations fail diagnostics without changing harness, provider, or billing source.
- Add deterministic configuration diagnostics plus explicitly invoked live probes. Capture actual provider/model identity when exposed and label it unavailable otherwise. Unknown usage/pricing stays unknown.
- Produce a machine-readable and human-readable compatibility matrix with adapter/harness versions, protocol, auth type, model, evidence, and support status. Upstream documentation alone is not a passing test.

Exclude persistent Codex subscription refresh/secret writeback, arbitrary credential discovery, per-internal-call proxies, and automatic model selection; 05 selects among these tested profiles.

## Checkpoints

1. **Contract suite:** extract reusable ACP/profile acceptance fixtures from 01 and test malformed/unsupported negotiation and permissions.
2. **Adapters and isolation:** complete all three harness paths and authentication boundaries.
3. **Endpoint matrix:** exercise native protocols and router presets against recording test servers, then real configured endpoints.
4. **Hosted qualification:** run bounded real-agent tasks in the consumer execution environment and publish compatibility evidence and installation guidance.

## Acceptance

- [ ] **03-A:** Each of Copilot, Codex, and Claude completes a real isolated fixture edit, permitted tool execution, streamed response, and follow-up turn. At least one hosted execution per harness proves Actions compatibility.
- [ ] **03-B:** Each adapter denies prohibited access, handles protocol errors, cancels a turn, and terminates contained processes on timeout. Test full process cleanup, not merely a returned cancellation message.
- [ ] **03-C:** Recording endpoints prove protocol-specific URL roots, headers/auth source, streaming, tool continuations, and response handling. Native subscription credentials never reach custom/router endpoints.
- [ ] **03-D:** Required router targets are attempted with real configured resources: Copilot/OpenRouter, Codex/OpenRouter including the free-only preset, Claude/OpenRouter with a compatible Claude model, and Copilot/Codex against the provided FreeLLMAPI service. Supported entries must complete a tool-using fixture, not just a text response.
- [ ] **03-E:** An observed upstream incompatibility is recorded with reproducible evidence and an explicit unsupported/experimental status; absent access is recorded as untested and leaves required live validation pending. Do not silently remove a planned target or replace a required harness. A promised required capability remains open until it works or Kevin explicitly changes its scope; labeling it unsupported is not by itself completion. Targets already designated experimental by the plan may remain experimental with evidence.
- [ ] **03-F:** Free-only, quota, auth failure, unsupported effort/model, context limits, and endpoint unavailability cannot trigger a paid or alternative-auth fallback. Ordinary diagnostics use zero model calls.
- [ ] **03-G:** Profile selection from trusted configuration/inputs has documented precedence. Issue text and repository agent plugins cannot supply credential-bearing endpoints or override policy.

## Evidence and handoff

Write `docs/milestones/evidence/03-harnesses.md` and the compatibility matrix. Document exact test versions, redacted profile names, tested destinations, usage/budget limitations, unsupported capabilities, and the profile-selection interface consumed by 04/05. Do not call the first release complete while a required harness lacks live evidence.

## Goal prompt

```text
Implement milestone 03 in docs/milestones/03-harnesses.md under the shared contract in docs/milestones/README.md and PLAN.md. Complete the three Caelis-backed ACP harness integrations, isolated auth/profile selection, and direct endpoint compatibility matrix. Use subagents for independent adapters or contract tests, integrate their work, and maintain docs/milestones/evidence/03-harnesses.md with actual local and live results. Do not substitute mocks for live interoperability, forward subscription credentials to routers, or enable paid fallbacks. Stop when this milestone's required acceptance is complete; leave unavailable or incompatible targets accurately documented.
```
