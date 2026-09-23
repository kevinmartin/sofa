# Milestone 06: Adaptive testing and progress supervision

## Outcome and dependencies

Extend independent acceptance testing with bounded Jev-driven browser exploration in disposable environments. Produce reproducible defects and independently verified outcomes. Add an advisory progress supervisor that identifies stalled or off-track work without replacing deterministic timeouts or mandatory gates.

Depends on **04: mandatory independent gates** and **05: System One routing/recovery**. Read [the shared goal contract](README.md), [the main plan](../../PLAN.md), [System One controllers](../system-one.md), and [blackbox validation](../lifecycle.md). Reuse the existing gate/browser runtime boundary, Go decision provider, Caelis-backed ACP sessions, task ownership, evidence, and budgets.

## Scope and exclusions

Implement all new observation, decision, action-policy, supervision, and reporting logic in Go. Use the existing browser runtime against a local/disposable application with synthetic accounts and a declared origin allowlist. Support observed HTML/ARIA controls through click, fixture-backed fill, native select, scroll, bounded wait, done, and blocked operations. Unsupported controls stop or escalate explicitly.

Keep deterministic regression journeys and acceptance oracles authoritative. Jev consumes DOM/accessibility text; screenshots support evidence, not unsupported Jev vision. Novel generated test text may use an already supported generative profile only after deterministic fixtures are exhausted and within the same budget.

Exclude production targets, arbitrary web browsing, payment or messaging actions, new browser services, general desktop/mobile control, per-call model proxies, and automatic semantic worker termination. Progress supervision remains advisory in this milestone; deterministic cancellation already implemented remains active. Active semantic intervention requires a later evaluated policy change.

## Checkpoints

1. **Observed action boundary.** Produce bounded observations containing allowed node IDs, compatible operations, current page/document generation, and public state. Batch operation and compatible-target questions when useful. Execute only the selected operation's target. Revalidate origin, document, target existence/enabled state, and budget immediately before action; consume decisions once and record execution before observing again. Model output never becomes executable code or selectors.
2. **Independent exploratory gate.** Accept the approved mission, fixture dataset, immutable oracles, and candidate artifact/environment identity from milestone 04. Keep source and implementer reasoning outside the tester workspace. Capture action traces, screenshots, network/console errors, and inputs. `DONE` triggers independent assertion evaluation; incomplete exploration becomes Blocked when required, not Passed. Confirmed failures produce replayable deterministic regression cases.
3. **Bounded supervision.** Project ACP events, elapsed time, changed paths, test outcomes, and clipped output into a compact observation. Evaluate progress/drift only after meaningful new evidence, with debouncing and a persisted call ceiling. Record suggested continue/checkpoint/escalate outcomes in shadow mode. Provider errors preserve the worker's ordinary path and deterministic limits; no unverified live-steering hook is introduced.
4. **Evaluation and real browser run.** Create a local fixture application with known-good and seeded-defect versions covering role separation, invalid input, persistence, asynchronous updates, and stale/replaced controls. Freeze missions, expected oracles, budgets, and evaluation targets before held-out runs. Compare deterministic journeys with the exploratory lane on defect discovery, false alarms, false completion, actions, runtime, and inference usage. Run the actual browser with live Jev, including an end-to-end discovered failure → deterministic reproducer → repaired candidate → passing oracle demonstration.

## Observable acceptance

- [ ] **06-A:** Browser execution reaches only configured disposable origins with synthetic data; source, publisher credentials, and production credentials are unavailable to the tester.
- [ ] **06-B:** Stale/replaced/disabled nodes, navigation, duplicate deliveries, and interrupted observations cannot reuse an already consumed action or silently click another target.
- [ ] **06-C:** Changed page text cannot create new operations, targets, credential access, or permission through prompt injection.
- [ ] **06-D:** Fixed scenarios execute without model calls; unsupported controls, unavailable Jev, exhausted budgets, and inconclusive evidence return explicit outcomes without replacing required exploration with an invented pass.
- [ ] **06-E:** Seeded defects fail independent oracles even when the decision provider returns `DONE`; passing evidence matches the tested candidate and environment.
- [ ] **06-F:** At least one live exploratory finding yields a deterministic replay that fails the buggy fixture and passes its repaired version.
- [ ] **06-G:** Repeated identical supervision evidence makes no new decision calls; advisory errors do not stop useful workers or waive deterministic limits.
- [ ] **06-H:** Real browser and live-provider evidence is distinct from mocked replay coverage; unavailable required live validation stays incomplete while independent work continues.

## Evidence and handoff

During implementation, write `docs/milestones/evidence/06-adaptive-testing.md` with fixture versions, frozen missions/oracles, browser/provider/harness versions, commands, live traces, reproduction cases, outcome/cost tables, and unsupported cases. Include supervision false-positive/negative observations without claiming calibrated control authority. Scrub captured credentials and unrelated data. Hand later memory/watch milestones structured findings and episode-ready evidence references. Do not create this evidence report during planning.

## Paste-ready goal

```text
Implement docs/milestones/06-adaptive-testing.md under docs/milestones/README.md. Complete bounded browser exploration, advisory supervision, independent oracle validation, and the specified evidence report. Keep unavailable required live validation explicitly incomplete while continuing independent work. Stop at this milestone.
```
