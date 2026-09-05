# Tier 3 browser and Patreon tests

Run `bash scripts/test-tiers.sh 3`. Every model-driven test uses the `TestLLM`
prefix and `RUN_COMPUTER_LLM_TESTS` gate. The runner selects that prefix, so
the saved-context Patreon editor regression is now included in discovery.

The suite includes model-driven semantic navigation, masked dates, dropdown
failures, download lifecycle, consequence guards, session arguments/lifetimes,
outcome waits, and these Patreon cases:

| Test | Independent verification |
| --- | --- |
| `TestLLMPatreonReliabilityFixtureLive` | Long audience label and schedule switches enabled; final scheduling remains uncommitted |
| `TestLLMPatreonRealContenteditableLive` | Exact body edit/readback/restoration and paywall preservation in the saved draft |
| `TestLLMPatreonMediaPublishLive` | Model navigates the composer and publishes a video test post; final URL, title, and media checked independently |
| `TestLLMPatreonSchedulingLive` | Model configures audience/date/time, commits final Schedule, verifies scheduled status after reload, and checks session survival beyond five minutes |

The deterministic `TestComputerPatreonRealSite` and
`TestComputerPatreonRealMediaPublish` scripts remain available separately.
The new tier 3 versions let the model choose actions rather than replaying
their scripted control selections.

## Saved test account profile

Configure only a disposable creator and draft. The media test publishes a post;
the scheduling test commits a scheduled post. The editor test restores the supplied
original body. These are real network/browser tests, not a simulated Patreon
page. Without account configuration, the account cases skip; the local fixture
still runs.

For a complete profile, set:

```sh
export COMPUTER_TIER3_REQUIRE_PATREON=1
export COMPUTER_PATREON_CREATOR_URL='https://www.patreon.com/c/TEST_CREATOR'
export COMPUTER_PATREON_DRAFT_URL='https://www.patreon.com/TEST_CREATOR/posts/TEST_DRAFT/edit'
export COMPUTER_PATREON_ORIGINAL_TEXT='Exact existing disposable draft body'
export COMPUTER_LLM_CODEX_BIN='/path/to/current/authenticated/codex'
export COMPUTER_LLM_ARTIFACT_DIR='/absolute/path/to/test-evidence'
```

To test this checkout, supply `BROWSERBASE_API_KEY`, `BROWSERBASE_PROJECT_ID`,
and `COMPUTER_PATREON_PROVIDER_CONTEXT_ID` from the existing test connection.
The harness builds this checkout, starts isolated sidecars with temporary
databases, imports the saved provider context, and closes browser sessions.
It does not replace the installed application. Credentials stay in the process
environment; do not commit them or put them in test logs.

Alternatively, configure `COMPUTER_PATREON_CONTEXT_ID` with an installed-app
endpoint (`COMPUTER_MCP_ENDPOINT`, `COMPUTER_MCP_TOKEN`, optional
`APTEVA_PROJECT_ID`), or use the local Apteva configuration supported by
`newLocalComputerMCPClient`. This validates the installed binary, so check its
version before interpreting the result as checkout validation.

Set `COMPUTER_LLM_BROWSER_BACKEND=browserbase` to run provider-capable LLM
fixtures on Browserbase too. Other local-only LLM fixtures continue locally.
The default model is `gpt-5.6-terra`; `COMPUTER_LLM_TEST_MODEL` can override it.
Browser-heavy test packages run serially to avoid interference with rendering
and timer assertions. The scheduling test first validates the prepared draft, then lets the model
perform the final Schedule action in a separate phase. It verifies the immutable post ID, exact title field value, and scheduled
date/time both before and after reload. Cosmetic URL slugs may change.
The scheduling lifetime check waits until at least
6 minutes 15 seconds after opening its browser, counting workflow time.

```sh
bash scripts/test-tiers.sh 3
```

The required-profile option fails before starting if account configuration is
incomplete. Individual cases can also be run with `GOWORK=off
RUN_COMPUTER_LLM_TESTS=1 go test -v -run '^TestLLMPatreon' -timeout 45m .`.

## Observing the agent

The autonomous workflows save each screenshot, semantic state, model decision,
and tool result under the artifact directory. Model decisions and action timing
also appear in the Go test output. Binary payloads and connection URLs are
excluded from JSON evidence. Each decision receives the current screenshot and
tool schema plus compact prior action results and observed field values.
Historical field observations omit target IDs and coordinates; they preserve
verification evidence when the next screenshot scrolls those controls out of
view. The assertion harness may scroll to read offscreen date/time controls,
but never changes the values the model configured. MCP action errors return to the
model so it can inspect and recover; transport failures fail the test.

Tests do not accept a model's `done` claim as proof. Browser state is checked
afterward, and each workflow has a bounded action budget. Preserve failed-run
artifacts separately when comparing a fix with a rerun.
