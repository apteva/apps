# Tests

Apteva sidecar for project test suites, multi-step scenarios, durable run
history, and a native Tests panel. Version 0.4.0 uses app-sdk v0.115.1.

Standalone suites keep the original HTTP and Function checks. Scenario checks
can chain steps and capture values. An isolated release suite binds an
Environments definition and produces a verdict for an exact candidate.
Agent behavior evaluations remain in the Evals app.

## Installation

Select the optional **Functions** dependency when installing Tests if you want
function checks. HTTP-only installations can omit it. An already installed
Functions app still needs an explicit binding; select it in the Tests
installation’s App dependencies settings. API installers pass
`bindings: {"functions": FUNCTIONS_INSTALL_ID}` in the install request.

## Example

Create a suite with `tests_suite_save`:

```json
{"name":"Checkout smoke tests","environment":"staging","description":"Price and endpoint contracts"}
```

Use the returned suite id in `tests_check_save`:

```json
{
  "suite_id": 1,
  "name": "Two items cost 120",
  "kind": "function",
  "definition": {
    "function": "calculate-price",
    "event": {"quantity": 2},
    "timeout_ms": 10000,
    "assertions": [{"path":"/response/total","op":"equals","value":120}]
  }
}
```

HTTP check:

```json
{
  "suite_id": 1,
  "name": "Health endpoint",
  "kind": "http",
  "definition": {
    "url": "https://your-staging-app.example/health",
    "method": "GET",
    "timeout_ms": 10000,
    "assertions": [
      {"path":"/status_code","op":"equals","value":200},
      {"path":"/json/healthy","op":"equals","value":true}
    ]
  }
}
```

Call `tests_run` with `{"suite_id":1}`. It returns a queued run immediately.
Poll `tests_run_get` with its `id`, or open the panel's Run history. A caller
can supply `request_key` to reuse the same run on a retried submission. The
same key cannot be used for a different suite within the project.

Checks default to enabled. `tests_check_save` is a full replacement on update;
send all fields and the complete definition. Disabling/deleting a check or
archiving its suite does not alter already queued runs. Suite history remains
queryable with `tests_runs_list` even after archive.

## Isolated release suites

Bind Environments 0.8.0 or newer in the Tests install. Set up an Environments
definition with explicit `network_mode: block`, `integration_mode: mock`,
selected app installs, and a 60–900 second TTL. Use seeds and synthetic fixtures
to provision the runtime. Tests rejects live connections, managed MCP servers,
and outbound allowlists for release suites. The runtime edge blocks managed
outbound HTTP. Use trusted app code in this runtime.

Create a suite with a `release` configuration. Use the definition's ID and
`spec_version`, the complete set of pinned runtime app versions including
dependencies, and a public Gateway route prefix.

```json
{
  "name": "Items release suite",
  "release": {
    "environment_id": "env_items",
    "environment_revision": 3,
    "app_versions": {"api": "0.8.1", "items": "1.2.0"},
    "gateway_app": "api",
    "gateway_path": "/gw/items/v1",
    "candidate_probe": {"app": "items", "tool": "items_loaded_digest", "path": "/digest"},
    "read_tools": {"items": ["items_loaded_digest", "items_find"]},
    "identities": {
      "writer": {
        "app": "api", "tool": "api_key_create",
        "input": {"api_slug": "items", "name": "scenario"},
        "path": "/secret", "header": "X-API-Key"
      }
    }
  }
}
```

The fixture loader may reference `{{candidate_digest}}` in seed inputs to
resolve a content-addressed candidate. The probe must independently measure
the artifacts and configuration loaded in the runtime. Tests compares that
measurement before and after execution. When using Deploy, have the probe
return the same artifact digest as `deploy_candidate_digest`. Keep the loader
and probe under release policy control.

Named identities resolve inside the runtime. Use `Authorization` with an
optional `Bearer ` prefix, `X-API-Key`, or `Cookie`. Bearer identities require
the server's runtime public authorization forwarding support. The runner
fails if the runtime Gateway cannot confirm that forwarding. Use fixture-only
credentials and read-only tools in the reviewed `read_tools` allowlist.

Save a check with `kind: scenario` and a definition such as this one.

```json
{
  "steps": [
    {
      "id": "create",
      "kind": "http",
      "method": "POST",
      "path": "/items",
      "identity": "writer",
      "json": {"name": "Synthetic item"},
      "assertions": [
        {"path": "/status_code", "op": "equals", "value": 201},
        {"path": "/json/id", "op": "exists"}
      ],
      "capture": {"item_id": "/json/id"}
    },
    {
      "id": "read",
      "kind": "http",
      "path": "/items/{{item_id}}",
      "identity": "writer",
      "assertions": [{"path": "/json/name", "op": "equals", "value": "Synthetic item"}],
      "evidence": ["/json/name"]
    }
  ]
}
```

- Use `function` with a Function name and event, or `app_tool` with an
  allowlisted app, tool and input. Release steps address runtime apps only.
- Capture a JSON pointer with `capture`. A whole-field reference in JSON or
  tool input preserves its type. Capture response headers using lowercase keys
  such as `/headers/location`. An absolute Location must belong to the same
  runtime Gateway. Redirects are returned without following.
- Use `poll_every_ms` on GET, HEAD or read-tool steps. Use a step with an ID
  and a `parallel` array of 2–4 leaves to start competing requests together.
  Siblings read prior captures and publish their own captures after the group.
- Use `response_schema` for an inline JSON Schema of the decoded HTTP response.
  External schema references are disabled. Select safe fields for evidence with
  JSON pointers. Scenario evidence records step status, duration, request ID,
  assertion outcomes and selected values. Credential fields and resolved
  credential values are redacted.
- Optional `release.openapi` accepts an OpenAPI 3 JSON document. Coverage
  matches observed HTTP method and path to operation IDs. Only successful
  responses in passing scenarios with a response assertion or schema count.
  A 204 may instead be followed by a state assertion. Missing operations fail
  the release suite.

Queue with `tests_run` and the candidate's SHA-256 digest. Use
`tests_release_spec` to get the current suite and fixture revisions for a
deployment policy. Pass those pins to `tests_run` to require a specific
revision. The queued run freezes its definitions and fixture. Inspect it with
`tests_run_get` or cancel it with `tests_run_cancel`. A release controller
queries `tests_release_verdict` with `suite_id`, `candidate_digest`,
`suite_revision` and `fixture_revision`. Completion events are useful wakeups.
The stored verdict governs promotion.

## Assertions and evidence

Paths are JSON pointers: `/response/items/0/id`, `/json/healthy`, `/body`,
`/status_code`. Escape `/` inside a key as `~1`, and `~` as `~0`. Empty path
selects the entire runner output. Numeric array indices use canonical decimal.

Operators: `equals`, `not_equals`, `exists`, `contains` (string substring),
`lt`, `lte`, `gt`, `gte` (numbers). Missing paths fail all operators, including
`not_equals`. Present JSON null differs from a missing path. Equality compares
JSON values without string/number coercion.

Function evidence preserves invocation id, status, duration, logs/error fields,
and the decoded JSON response. An invocation must have status `ok` before its
assertions can pass. HTTP evidence has `status_code`, raw `body`, and parsed
`json` (null if not JSON). Non-2xx HTTP statuses are assertion inputs, so a
check can deliberately expect 404. Redirects are returned without following.

Every queued run stores its check definitions and suite name/environment.
Results distinguish assertion failure (`failed`) from execution error
(`error`); either makes the suite fail. Errors do not stop subsequent checks.
Assertion values larger than 8 KiB are previewed in per-assertion evidence;
comparison still uses the full value and the full output is stored once.

## App bus and scheduling

The manifest declares `tests.suite.saved`, `tests.suite.archived`,
`tests.check.saved`, `tests.check.deleted`, `tests.run.queued`, and
`tests.run.completed`. Event payloads include entity ids; completion also
includes status, counts, timestamps, environment, and any run-level error.
They omit function inputs, HTTP headers and response evidence.

Changes and outgoing events commit in one database transaction. An outbox
worker retries delivery until the platform acknowledges it. Delivery is
at least once; deduplicate on `(source_install_id, event_id)` if a consumer
requires exactly-once effects. Publication uses the SDK’s acknowledged event emitter.

For scheduled runs, configure a Jobs `app_tool` target for app `tests`, tool
`tests_run`, input `{"suite_id":1}`. Do not reuse a constant `request_key` across
distinct scheduled occurrences. A calling app must declare `platform.apps.call`
and a Tests app dependency/binding.

The event handler also accepts `tests.run.requested` with a project id and
`data: {"suite_id":1}` when a platform event subscription is wired to this app.
Stable `delivery_id` values deduplicate retries. There is no automatic
subscription to another app's deploy events in this release.

## Persistence and execution

SQLite tables: `test_suites`, `test_checks`, `test_runs`, `test_results`, and
`test_outbox`. Every data operation is project-scoped; authenticated request
scope takes precedence over user arguments. A project-specific installation
cannot be switched to a different project.

The SDK runner worker claims a queued run atomically, then executes checks
sequentially. Limits: 50 checks per suite, 50 assertions per check, 100 pending
runs per project, 256 KiB definitions/runner responses, 100–30,000 ms per check,
and a five-minute suite deadline. Scenarios allow 24 leaves per check and a
120-second check deadline. Release suites accept up to 1 MiB of configuration. Runs interrupted by a sidecar restart are
marked failed, not replayed, since a check may have side effects. Queued runs
resume after restart. Environment teardown is retried after interruption, with
the platform TTL as a fallback. This assumes one sidecar process per installation.

A standalone function-check timeout does not terminate the downstream function. Its own
Functions timeout still applies. Outstanding calls remain bounded to four
per sidecar until they complete. Avoid using test checks for uncontrolled
production writes. No automatic retries are performed on failing checks.

HTTP requests do not forward sidecar credentials, use proxy environment
variables, or follow redirects. Private, loopback, link-local and shared-address
targets are blocked by default, including after DNS resolution. For intentional
local/internal service testing the operator may set
`APTEVA_TESTS_ALLOW_PRIVATE_NETWORK=true` on the sidecar. This permits access
to internal addresses and should be enabled only for trusted projects.

Check headers, request bodies, and response evidence are persisted in the app
database and visible to authorized project users. Prefer dedicated test data
and credentials. History is retained until operator-managed database cleanup;
automatic retention is operator-managed. Scenario evidence uses field selection
and redaction, with legacy checks retaining their original evidence format.

## HTTP and UI

`POST /tools/call?project_id=PROJECT` accepts
`{"tool":"tests_suites_list","args":{}}`. All routes require the SDK's normal
token authentication. The platform proxies this under `/api/apps/tests`.
`tests_run` returns HTTP 202. The UI includes `install_id` when addressing a
specific installation and polls active runs without blocking mutations.

`ui/TestsPanel.tsx` is bundled into `ui/TestsPanel.mjs`; the SDK serves it and
the theme-aware SVG icon automatically. React comes from the dashboard's
shared import map.

## Development

```sh
# From apps/mcp/tests; GOWORK=off verifies the published dependency pin.
GOWORK=off go test -race ./...
GOWORK=off go build .

# From apps/
bun run scripts/build-panels.ts --app tests
```

For a local sidecar, set `APTEVA_PROJECT_ID`, `DB_PATH`, `APTEVA_APP_PORT`,
`APTEVA_GATEWAY_URL`, and the app token supplied by the platform. No credentials
are stored in this repository.

## Browser checks (0.3.0)

Bind optional **Computer >=0.9.3** and **Environments >=0.9.0** to Tests.
Computer needs Chromium/Chrome installed on its host (`CHROME_BIN` can select
an executable). The initial backend runs local headless Chromium, one check
at a time, with a fresh profile per check. Evidence includes the actual browser
engine, viewport, locale and timezone. Recordings are not enabled in protocol v1.

Add named public paths to the pinned EnvironmentSpec:

```json
{"targets":{"frontend":{"app":"site","path":"/ui"},"api":{"app":"api","path":"/gw/test"},"auth":{"app":"auth","path":"/login"}}}
```

Seeds may use `{"$target":"api"}` to inject the freshly provisioned URL and
`{"$ref":"2.client_id"}` to use earlier seed results. Seed users and data in
the isolated runtime. All UI assets, API calls and Auth routes must resolve
under the named runtime paths; use a runtime-scoped or data-URL favicon too.
The forward proxy denies traffic outside these paths, including redirects,
frames, WebSockets, service workers and other runtime/management endpoints.
Targets currently use the server's local HTTP runtime endpoints.

Add a browser configuration to the existing release suite:

```json
{"browser":{"width":1280,"height":720,"locale":"en-US","timezone":"UTC","value_refs":{"operator.email":{"app":"fixture","tool":"credentials","path":"/email"},"operator.password":{"app":"fixture","tool":"credentials","path":"/password"}}}}
```

Value references resolve against seeded runtime apps and are redacted from
evidence. Keep credentials out of suite definitions. The release candidate
probe should measure a bundle manifest covering UI, API and configuration
artifacts. The verdict also pins suite/fixture revisions and runtime app
versions. Changing pinned inputs requires a new passing run.

Create a browser check or mix browser and backend steps in a scenario:

```json
{"name":"Sign in and dashboard","kind":"browser","suite_id":1,"definition":{"timeout_ms":60000,"steps":[
  {"id":"open","browser":{"action":"navigate","target":"frontend","path":"/login"}},
  {"id":"email","browser":{"action":"fill","locator":{"test_id":"email"},"value_ref":"operator.email"}},
  {"id":"password","browser":{"action":"fill","locator":{"test_id":"password"},"value_ref":"operator.password"}},
  {"id":"submit","browser":{"action":"click","locator":{"role":"button","name":"Sign in"}}},
  {"id":"dashboard","poll_every_ms":100,"browser":{"action":"observe","locator":{"role":"heading","name":"Dashboard"}},"assertions":[{"path":"/visible","op":"equals","value":true}]}
]}}
```

In a `scenario`, give these steps `"kind":"browser"`. Actions are `navigate`,
`reload`, `fill`, `click`, `select`, `check`, `upload` and `observe`. Locators
accept one `test_id`, accessible `role`/`name`, or exact `text`. Observations
expose `count`, `text`, `value`, `visible`, `enabled`, `checked`, `target` and
relative `path`; password and sensitive values are omitted. Assertions use the
existing JSON-pointer operators. Input waits for one visible enabled element,
then dispatches once. Only observations may poll. Parallel browser steps are
rejected. Use `checked` for checkboxes and `upload_ref` referencing a named
`{name,mime,base64}` fixture in `release.browser.uploads` for uploads.

Failures retain masked PNG evidence owned by Tests, served through the
authenticated project-scoped artifact route. Inputs, sensitive elements and
known secret text are masked. Use synthetic fixture data for application content.
`artifact_retention_days` accepts 1–90 (default 7); run history stays available.
Page exceptions, console errors, attempted out-of-boundary page requests or
incomplete diagnostics fail the check. Chromium's own denied background
requests are recorded separately as `boundary_denied`. HTTP error statuses
remain assertable, including permission-denied acceptance cases.

Browser failures, missing capabilities, timeouts and cancellations prevent a
passing verdict. Each run attempt creates a fresh environment and each browser
check gets a fresh profile. Persisted session IDs, TTL expiry and restart
reconciliation ensure browser cleanup.

Run real local Chrome boundary and input tests with:

```sh
cd mcp/computer
RUN_BROWSER_TESTS=1 GOWORK=off go test -count=1 ./internal/browser/testbrowser
```

The complete local sidecar smoke test uses real Auth/API/Environments/Tests/
Computer and Chromium, an isolated SQLite fixture and a measured UI/backend
source digest. It covers login, save, retry without duplicates, reload,
backend read assertions, a failing screenshot/verdict and cancellation cleanup:

```sh
APTEVA_SERVER_SOURCE=/absolute/path/to/server mcp/tests/testdata/local-browser/run.sh
```

The server checkout needs runtime public proxy authorization preservation
(server commit `4cf671114d0b2d935ee1fb5a211913bb21398289` or newer). The script
uses a temporary Go overlay and temporary server/app databases; it does not
connect to a running instance.

### Explicit browser values (0.3.1)

A `select` action requires exactly one literal `value` string or valid
`value_ref`. `"value":""` selects the empty-valued option and is retained when
checks are saved, loaded and copied into run snapshots. Missing, null,
non-string and literal-plus-reference selections are rejected. Runtime browser
value references may resolve to the empty string; values are expanded and
forwarded without trimming or fallback. Credential and candidate digest probes
still reject empty results. An omitted `fill` value retains its existing clear
behavior. Browser suites require Computer 0.9.3 or newer for empty selection.

## Home widgets (0.4.0)

Add **Tests overview** and **Recent test failures** from the Home widget gallery.
Both are project-scoped and support half/full width. Overview is suggested;
Home placements remain an operator choice. Existing Tests panels and tools stay
available.

- Overview shows run count, pass rate, failed runs, active runs, recent results,
  and a daily trend at full width. Choose the last 24 hours or 7 days and filter
  by suite/environment. Statistics query the whole period, independently of the
  existing 100-run history page. Cancelled runs are separate and excluded from
  the pass-rate denominator. Active now includes older queued/running attempts.
- Recent failures shows failed checks, the first failed step, a short saved
  error, repeated-failure counts and screenshot availability. Grouping combines
  attempts of the same suite/environment/check; ungroup to see every failed
  attempt. Run setup errors appear even when no check result was saved.
  Cancelled runs are excluded. Failed attempts stay visible after later passes.
- Rows open the specific saved run and evidence. Archived suites remain
  readable through these links, while running/editing archived suites stays
  disabled. Expired screenshots lose their indicator; run history remains.

Filters are available in the widget; settings optionally choose starting
suite/environment, time window, maximum full-width rows and failure grouping.
Widgets refresh on Tests events, window focus, and a 30-second polling fallback.
Empty/cancelled-only periods have no pass-rate percentage. Loading errors hide
stale statistics until a successful retry.

Authenticated read-only endpoints are `GET /dashboard/overview` and
`GET /dashboard/failures`, through the normal app proxy. Query parameters:
`project_id`, `hours` (24 or 168), optional `suite_id`/`environment`, `limit`
(1–10), and `group` (true/false). They return saved summaries without check
inputs, source/fixture snapshots, output bodies or credential values. Trend
buckets use UTC; durations use start/finish timestamps. Filters use the run's
captured environment. A dashboard pass rate is historical context; deployment
continues to use the existing candidate-pinned release verdict.

Local verification (no running Apteva instance or credentials required):

```sh
(cd mcp/tests && GOWORK=off go test -race ./... && GOWORK=off go vet ./...)
bun install --frozen-lockfile
bun test mcp/tests/ui/dashboard.test.ts
bunx --no-install tsc -p mcp/tests/tsconfig.widgets.json
bun run scripts/build-panels.ts --app tests
CHROME_BIN=/absolute/path/to/chrome bunx --no-install playwright test --config mcp/tests/playwright.config.ts
```

The browser harness hosts the real built widgets and Tests panel with a local
SQLite-backed app fixture on loopback ports 4337/4338. It exercises filters,
complete statistics, saved-run/screenshot navigation, archived history,
refresh/error/empty states, project changes, light/dark themes and 240px
containers. `TESTS_HOST_VENDOR` and `TESTS_HOST_CSS` can point to a local dashboard
build to verify its actual vendor modules and stylesheet.

## Suite targets, credentials and sessions (0.5.0)

A suite can declare a `target` instead of a hand-written `release`. Suites
without either keep the earlier behaviour.

### Existing mode: deployed endpoints

```json
{"name":"Staging smoke","target":{"mode":"existing",
  "targets":{"frontend":{"base_url":"https://staging.example.com/ui"},"api":{"base_url":"https://staging.example.com/api"}},
  "candidate":{"deployment_id":12}},
 "credentials":{"operator":{"ref":"operator"}},
 "session":{"kind":"browser","setup_check":"login"}}
```

- Checks address targets by name and a relative path: HTTP checks use
  `{"target":"api","path":"/health"}` (instead of `url`), scenario HTTP steps
  use `"target":"api"`, browser steps `navigate` with `target` and `path`.
- `candidate` names a Deploy deployment. Tests resolves the live build and its
  digest through Deploy when the run is queued, checks it again before the
  checks start, and after they finish. If the live build changed, the run is
  marked inconclusive and produces no passing verdict. Only `"build":"live"`
  (the default) is accepted here; an existing-mode verdict needs a candidate.
- The verdict's `fixture_revision` is a digest of the mode, targets and
  candidate deployment; `tests_release_spec` returns it.
- HTTP requests use the normal outbound client (private addresses still need
  `APTEVA_TESTS_ALLOW_PRIVATE_NETWORK=true`).

### Browser checks in existing mode

Existing-mode browser checks use Computer's regular browser tools
(`browser_session`, `computer_use`, `browser_screenshot`) rather than the
isolated test browser. Consequences:

- Locate elements with `test_id` (matched as `[data-testid="…"]`) or a CSS
  `selector`. `text` locators work with `observe` only; `role` locators are
  rejected.
- Observations expose `/visible`, `/path`, `/url`, `/target` and
  `/action_dispatched` (plus `/checked`, `/current_value`, `/text_value`,
  `/verified` from non-sensitive inputs). Element counts and exact element
  text are not available.
- Computer reports no console errors or failed sub-requests for regular
  sessions, and pages are not network-restricted.
- Failure screenshots are not masked by Computer, so Tests skips the
  screenshot for any check that entered a credential.

### Credentials

Put values in the Tests install's encrypted **Test credentials** setting:

```json
{"<project_id>":{"operator":{"email":"qa@example.com","password":"…"},"api":{"key":"…"}}}
```

Declare aliases on the suite (`"credentials":{"operator":{"ref":"operator"}}`)
and reference fields only through reference fields: browser
`"value_ref":"credentials.operator.password"`, or HTTP
`"header_refs":{"X-API-Key":{"ref":"credentials.api.key"}}` (Authorization,
Cookie and `X-` headers; `prefix` optional; requires a named target). Missing
values fail the run before any check starts. Every resolved value and session
token is replaced with `[redacted]` in stored results, errors and evidence.

### Sessions

- `{"kind":"browser","setup_check":"login"}` (existing mode): the setup check
  runs first in a new saved Computer browser context; every later browser
  check opens a fresh session from that context, so it starts signed in. The
  context record is deleted when the run ends. Computer keeps the local Chrome
  profile directory of a deleted context on disk.
- `{"kind":"bearer","setup_check":"login","capture":"token"}` (existing mode):
  the setup scenario captures a token; later HTTP steps and checks with a
  target receive `Authorization: Bearer <token>` (`header`/`prefix`
  configurable).
- A check opts out with `"session":"none"` in its definition.
- If the setup check fails, dependent checks are recorded as `blocked`. Runs
  report `blocked` counts; blocked checks fail the run and its verdict.

### Isolated mode: generated environments

```json
{"target":{"mode":"isolated","candidate":{"deployment_id":12,"build":"latest_succeeded"},
  "targets":{"api":{"app":"api","path":"/gw/items"}},
  "environment":{"apps":["database","functions","api","deploy"],"data":{"snapshot_id":"snap_1"},"ttl_seconds":900}}}
```

- Saving the suite creates (or updates) an Environments definition with block
  networking and mock integrations, and pins its `spec_version` and
  `spec_digest` (Environments 0.11.0 or newer). A changed definition fails the
  next run with `pinned v1/abc…, found v2/def…`; save the suite to re-pin.
- Runs start through Environments' pinned form with `seed_vars`
  `candidate_digest` and `candidate_build_id`, so seeds can load the candidate.
- The candidate build is resolved through Deploy when the run is queued and
  stored with it; a retry with the same `request_key` reuses it.
- With `deploy` in the environment, the runtime probe defaults to
  `deploy_candidate_digest` for that build; otherwise set `candidate_probe`
  (and optional `read_tools`). The probe still has to match before and after
  the checks.
- Runtime app versions are recorded from the runtime. Pin them with
  `environment.app_versions` to fail on drift.
- HTTP steps need a `target`. Browser checks use the isolated test browser,
  one fresh profile per check; browser sessions are not available in this mode.

### Other 0.5.0 changes

- Assertions accept `"decode":"json"` to follow a JSON pointer through a
  string-encoded value, such as a `functions_invoke` response.
- `budget_ms` on checks and steps fails a passing check or step that took
  longer, recorded as a `/duration_ms` assertion.
- Hand-written release suites report the exact fixture mismatch (wrong ID,
  wrong revision, empty spec, or spec over 256 KiB) instead of one generic error.
- Saving an enabled browser check verifies the Computer binding.
- `tests_run` accepts `candidate` (`{deployment_id, environment?, build}`) as an
  alternative to `candidate_digest` for target suites.

## Run selected checks, URL-rewriting apps, Deploy errors (0.6.0)

### Run selected checks

```json
{"suite_id":3,"check_names":["screen quality"]}
```

`tests_run` with `check_names` runs only those checks, whether enabled or not,
plus the suite's session setup check. Enabled flags are not changed. Unknown
names fail the request. These runs are marked `partial` and never produce a
release verdict, so `suite_revision`/`fixture_revision` are rejected with them.

### Apps that rewrite their URL on load

Existing-mode browser sessions now open on `about:blank`, so a check's first
`navigate` step is always a real URL change. A `navigate` to the page that is
already shown (for example when the app added `#/admin_dashboard` on load) is
accepted when the browser is on the destination apart from the `#…` part.

### Waiting for a page-ready signal

With Computer 0.14.0 or newer, read the status element directly (see 0.7.0
below). With older Computer versions, matching works only on visible elements
and attributes: have the page put the status in an attribute and match it
through a visible ancestor, e.g.
`body:has([data-testid="fx-api-test-status"][data-status="ready"])`.

### Deploy errors

- Saving a suite with `target.candidate` checks that Tests can read the Deploy
  deployment and asks you to bind Deploy if it cannot.
- Candidate resolution errors now include Deploy's own reason, for example
  `Deploy could not verify build 34: release artifact differs from attested build`.

## Element reads, page diagnostics, masked screenshots (0.7.0)

These features need Computer 0.14.0 or newer for existing-mode browser checks.
Tests checks once per run whether the bound Computer supports them; with an
older Computer it keeps the 0.6.0 behaviour.

### Element text and value

An `observe` step with a `test_id` or `selector` locator reads the element
with Computer's `read_element`, hidden elements included:

| Path | Value |
|---|---|
| `/count` | number of matches |
| `/visible` | true when any match is visible |
| `/text` | text of the first match |
| `/value` | form value of the first match |
| `/checked` | checkbox state of the first match |
| `/attributes/<name>` | attribute of the first match |
| `/redacted` | true when Computer withheld text or value (password and sensitive fields) |

Polling with `poll_every_ms` re-reads until the assertions pass or the step
times out:

```json
{"id":"ready","kind":"browser","poll_every_ms":250,"timeout_ms":20000,
 "browser":{"action":"observe","locator":{"test_id":"fx-api-test-status"}},
 "assertions":[{"path":"/text","op":"equals","value":"ready"}]}
```

Saving a check that asserts `/count`, `/text`, `/value`, `/redacted` or
`/attributes/*` is rejected when the bound Computer is older than 0.14.0
(`computer 0.14.0 or newer required for text assertions`); a step that asserts
them at run time against an older Computer fails with the same message.

### Page diagnostics

Every existing-mode browser session records page problems. After each browser
step, Tests reads what was recorded since the previous read and stores it in
the check's `output.browser.diagnostics` (`exceptions`, `console_errors`,
`requests` with method, origin, path, status, error and type; `truncated`).

Default rules:

- a page exception fails the check;
- a request to a declared target's origin that returns 400 or more, or fails
  without a response, fails the check (requests the browser aborted itself,
  `net::ERR_ABORTED`, are recorded only);
- requests to other origins and console errors are recorded only.

Override per check in `definition.diagnostics`, or turn it off with
`"diagnostics":"off"`:

```json
{"diagnostics":{"fail_on":["exceptions","target_requests"],
  "fail_origins":["https://api.example.com"],
  "allow":[{"path_prefix":"/api/telemetry","status":404}],
  "console_errors":"record"}}
```

- `fail_origins` adds origins whose failed requests fail the check, for an API
  served from a host that is not a declared target.
- `allow` exempts requests by path prefix, optionally only one status and one
  origin.
- `console_errors: "fail"` fails on any console error.
- With an explicit `fail_on`, truncated diagnostics also fail the check, since
  a dropped entry could hide a problem. They are always marked in the output.

A page that answers 401 before sign-in (for example `/api/me` on the login
screen) needs an `allow` entry on the login check.

### Masked failure screenshots

Failure screenshots are captured with `mask`: password and sensitive fields
are always hidden, every form field is hidden in a check that typed a
credential, and any text containing one of the run's credential values (up to
20, longest first) is hidden. If masking fails, Computer captures nothing and
the step records `screenshot_skipped: "mask_failed"`. Stored text is still
scrubbed to `[redacted]`.

### Sign-in profile cleanup

At run end the shared sign-in context is deleted with `purge_profile`, which
also removes the local Chrome profile folder and its cookies. If Computer
refuses because a session still holds the profile, Tests retries once; if that
fails too, the run records a warning in `warnings` and still passes or fails on
its checks.

## Code checks: the project's own tests (0.8.0)

A `code` check runs the project's test command on a source tree inside a
locked-down Workspaces test workspace and reads its JUnit report (and optional
lcov coverage). It needs Workspaces 0.8.0 or newer bound to Tests (and Tests
allowed by Workspaces' `test_workspace_apps` setting when it is set); Deploy
0.33.0 or newer to test a candidate's source; Code for Code-built candidates
and repository sources.

```json
{"suite_id": 4, "name": "bun test", "kind": "code", "definition": {"code": {
  "source": {"from": "candidate"},
  "prepare": {"argv": ["bun", "install", "--frozen-lockfile"]},
  "test": {"argv": ["bun", "test", "--reporter=junit", "--reporter-outfile=$TESTS_OUT/junit.xml",
                    "--coverage", "--coverage-reporter=lcov", "--coverage-dir=$TESTS_OUT/coverage"],
           "env": {"TZ": "UTC"}},
  "results": {"junit": "junit.xml", "coverage": "coverage/lcov.info"},
  "timeout_s": 900, "retry_flaky": true},
  "assertions": [{"path": "/coverage/lines_pct", "op": "gte", "value": 80}]}}
```

- **Source.** `from: "candidate"` tests the exact source of the run's Deploy
  build: Tests asks Deploy (`deploy_build_source`) where the source is, fetches
  the Code snapshot or Deploy's kept copy, checks the sha256 and computes the
  `workspaces-tree-v1` digest, which must equal what Deploy recorded and what
  Workspaces computes when it imports the archive. `from: "code"` exports the
  current state of a Code `repository` (optionally a `subdir`).
- **Commands.** argv runs without a shell. `$TESTS_OUT` in argv and env values
  is replaced by `/workspace/out`. `prepare` runs as the builder user (it may
  install dependencies; network `registry_only` by default, or `none` /
  `default`). `test` runs as the runner user, which can only write
  `$TESTS_OUT`, always without network. `working_directory` is relative to the
  source. Env names Workspaces sets (`HOME`, `TMPDIR`, `CI`, `TESTS_OUT`, …),
  names that look like secrets and values that reference credentials are
  rejected: code checks never receive suite credentials.
- **Results.** `results.junit` and `results.coverage` are relative to
  `$TESTS_OUT`. Bun, Vitest and Jest (jest-junit) reports are read, including
  Bun's nested describe suites. Bun's JUnit has no failure messages; the
  command log is saved with the run.
- **Outcome.** The check passes when the report has cases and none failed
  (apart from quarantined ones). It fails when cases fail, the command times
  out, the report is missing or empty, or the command exits non-zero while the
  report shows no failures. With `retry_flaky`, a failing test command runs
  once more in the same workspace; cases that pass on the retry are `flaky`,
  the check's status is `flaky` and the run fails (flaky is not a pass).
  Source and workspace problems are `error`.
- **Output** (assertable): `summary` (`tests`, `passed`, `failed`, `errors`,
  `skipped`, `flaky`, `quarantined_failures`), `coverage` (`lines_pct`,
  `functions_pct`, `branches_pct`, `delta_lines_pct` against this check's
  previous run), `exit_code`, `attempts`, `failures` (first 20), `slowest`
  (10), `commands` (status, exit code, seconds, `log_gap`), `source`
  (provenance below) and `artifacts` (ids of the saved JUnit report, lcov file
  and command log, served as plain text at `/artifacts/<id>`).
- **Provenance.** `source` records `source_digest`, `digest_method`,
  `build_id`, `source_kind`, the Code `repository` / `snapshot_id` /
  `source_revision`, `lockfiles`, the dependency `cache_key`, the workspace
  `image` and `workspace_id`.
- The workspace is destroyed after the results are exported (or when the
  check stops); if that fails the run records a warning and the workspace
  expires with its TTL (check timeout plus 15 minutes, at most 2 hours).

### Source-mode suites and release verdicts

`target: {"mode": "source", "candidate": {"deployment_id": 3, "build": "latest_succeeded"}}`
makes a suite that only runs code checks against a Deploy build. Code checks
with `from: "candidate"` also work in existing-mode suites with a
`target.candidate` (the live build); repository sources work in suites without
a target. Isolated suites do not run code checks.

A queued source-mode run is pinned to one build: `candidate` / `target.candidate`
resolves it, or `candidate_digest` alone (what the Deploy gate sends) finds the
build sealed with that digest. The release record carries `source_digest` and
`source`. A source-mode verdict needs a code check that tested the candidate's
source; Tests re-reads the build afterwards and the digest must be unchanged.

`tests_release_verdict` and `tests_run` accept `source_digest`: a verdict whose
run tested a different source is `source_mismatch` and not allowed (Deploy
0.33.0 sends it and also blocks on a mismatch itself).

`release_blocking: true` on a suite adds it to `tests_release_verdicts
{candidate_digest, source_digest?}`, which is allowed only when every
release-blocking suite has a passing, matching run for that candidate at its
current revisions; each entry has the run id, state and release record.
Release-blocking suites must test the candidate's own source (`from:
"candidate"`) and need a verdict source (a candidate, an isolated target or a
release configuration).

### Cases, history and quarantine

Every case from a code check's report is stored (up to 20,000 per check;
messages and stack traces up to 8 KB; rows kept 30 days). A case is identified
by `case_key` = `file :: describe > name`.

- `tests_cases_list {run_id | suite_id, check_id?, status?, file?, case_key?}`
  lists cases; `status: "failing"` selects failed and errored ones.
- `tests_case_history {suite_id, case_key?, unstable?}` shows runs, failures,
  flaky reruns, the last 20 outcomes (`p` passed, `f` failed, `e` error, `s`
  skipped, `y` flaky), the last failure and quarantine state.
- `tests_quarantine_set {suite_id, case_key, reason, days? (1–90, default 14) |
  expires_at?}` quarantines a case that has run in the suite; `remove: true`
  lifts it. A quarantined failure is recorded and counted in
  `quarantined_failures` but does not fail its check. Every quarantine
  expires. `tests_quarantine_list` lists them.

### Limits

- Workspaces imports at most 8 MB of compressed source (128 MB expanded,
  20,000 files, `node_modules` and `.git` left out); larger candidates fail
  with the compressed size.
- Code snapshots are kept 24 hours. After that, a Code-built candidate can be
  tested only while its repository is unchanged (it re-exports to the same
  snapshot id); otherwise rebuild. Deploy's kept copy of a local source
  follows Deploy's build retention, and its signed link must be reachable from
  Tests.
- A Deploy build that uses sibling Code dependencies records only the app
  folder; the siblings are not in the tested source.
- `prepare` runs as builder before the tests and could change the source; the
  digest is checked on import, not after `prepare`.
- Only the `bun-test` runtime profile is available.
- Command output is read through Workspaces' 64 KB window; when output is
  produced faster than Tests reads it, `log_gap` is set. Logs keep the last
  128 KB per command.
- The Tests panel and home widgets show code checks as plain results; a
  per-case view is planned for 0.9.0.
