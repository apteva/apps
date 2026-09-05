# Computer tier 3 observation and fixes

September 5, 2026. Based on Computer v0.7.87 in the isolated
`fix/computer-0.7.87-audit` worktree. The installed app was not replaced.

## Coverage added

The saved Patreon editor test now follows tier 3 discovery and configuration.
Autonomous media publishing, scheduling preparation, and a publishing-shaped
fixture run alongside the existing model-driven suite. The account tests use
Browserbase and the saved Test Patreon context against the disposable creator.
The model used was `gpt-5.6-terra` through the current authenticated desktop CLI.

Each autonomous step records the screenshot, semantic state, model decision,
and tool response. Assertions independently inspect the resulting page, rather
than accepting the model's completion claim. Browser release is also checked.
See [the test guide](TIER3_TESTS.md) for configuration and reproducible commands.

## Problems found by watching the runs

| Observation | Change |
| --- | --- |
| An asynchronous video preview moved Title after observation; input hit the old coordinates. | Structured text, date, selection, and upload actions resolve stable DOM identity and reject a disconnected target. |
| An overlay left covered settings controls and scroll panels in the semantic view; the model looped through menus until its 30-action budget expired. | Hit-test controls and panels, retain partially reachable controls, and omit fully covered ones. |
| Panel contents below the viewport were undiscoverable. | Include bounded control-name hints on scrollable panels. |
| Checkbox/radio state and ordinary field values were missing from observations. | Expose explicit true/false/mixed state and current values, without password values. |
| Free access appeared checked but reverted after a later page update. | Activate native inputs through their click behavior, so framework application state updates; do not merely assign the DOM property. Verify the result and preserve idempotency. |
| Switching the CDP attachment could leave the actual browser tab in the background. | Activate the browser target before attaching or reusing a tab. |
| Browserbase startup logs included authenticated debug URLs. | Remove those URLs from routine logs. |

These are shared browser behavior changes. They add no Patreon-specific logic,
new settings, or UI steps. Semantic hints are bounded and action history sent to
the model is compacted. They do not establish a whole-app speed benchmark.

## Validation

- **Tier 3: all 13 top-level LLM cases have passing results across the full run
  and targeted reruns.** The full `final.log` run passed 12 and exposed the
  native-radio persistence bug. `checked-fixed.log` passed the repaired
  scheduling workflow, its lifetime subtest, and the publishing fixture.
- **Media:** eight agent actions, one embed insertion, independently verified
  published title and loaded media. Published test post:
  [published media](https://www.patreon.com/u2430610/posts/computer-tier-3-168671400)
- **Scheduling:** the initial run exhausted 30 actions in a menu/scroll loop.
  The final repaired run completed in 12 actions, including an explicit return
  to verify Free access. Date/time were 2026-09-19 and 19:00; the draft remained
  unpublished. Session status was active at 377 seconds; release was confirmed.
  Inspection draft: [scheduling draft](https://www.patreon.com/u2430610/posts/168672356/edit)
- **Editor:** exact text restoration and paywall preservation passed against the
  saved test draft.
- **Tier 1:** the complete unit/race suite passed again after the radio fix
  (`tier1-radio-final.log`).
- **Tier 2:** passing across the full attempt and focused reruns. All four
  root/DOM extraction failures passed `local-recheck.log`; the complete local
  browser package passed `local-package-final.log` with the race detector.
- **Build/static checks:** `GOWORK=off go build ./...`, `go vet ./...`, and
  `git diff --check` passed.

The full tier 2 attempt encountered eight Chrome startup timeouts during severe
host contention (observed load average above 800), plus a caption-test observer
attached to the wrong DOM root. The four affected root/DOM extraction tests
passed targeted reruns; the caption observer now watches the document root.
The local browser package was rerun serially with `GOMAXPROCS=2 GOFLAGS=-p=1`.
These results do not claim a clean initial run or a whole-app latency gain.

New deterministic coverage checks layout-shift targeting, stale-target rejection,
file inputs, checked/mixed/value metadata, occluded panels, offscreen panel hints,
controlled-radio persistence and idempotency, and actual tab activation.

Useful logs: `final.log`, `checked-fixed.log`, `tier1-radio-final.log`,
`tier2-complete.log`, `local-recheck.log`, and `local-package-final.log`.
Step evidence is under `artifacts/final/` and `artifacts/checked-fixed/`;
`artifacts/expanded/` retains the original scheduling loop.

Evidence directory:
`/Users/marcoschwartz/Documents/code/.codex-audits/computer-patreon-tier3`

The media flow intentionally leaves published test posts, and scheduling leaves
drafts for inspection. The original editor draft is restored by its test.
No installation or deployment was performed during this initial validation.
Final scheduling commit coverage and the subsequent release are documented in
[Computer v0.7.88](RELEASE_0.7.88.md).
