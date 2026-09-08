# Tests

Apteva sidecar for project test suites, function and HTTP checks, durable run
history, and a native Tests panel. Version 0.1.0 uses app-sdk v0.76.0.

## Scope

Tests owns suites, assertions, queued runs, results, and evidence. Functions
executes the active handler through `PlatformAPI().CallAppResult`. Jobs can
schedule `tests_run`. Tests has no dependency on server internals.

This release supports **function and HTTP checks**. Browser/UI runners,
repository-backed test imports from Code, pre-activation function version
testing, and environment provisioning are future extensions. The suite's
environment is a label, not an isolation boundary: each check specifies the
actual function or endpoint it will exercise. Agent behavior evaluations
remain in the separate Evals app.

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
and a five-minute suite deadline. Runs interrupted by a sidecar restart are
marked failed, not replayed, since a check may have side effects. Queued runs
resume after restart. This assumes one sidecar process per installation.

The SDK's app-call API has no cancellation argument. A local function-check
timeout therefore **does not terminate the downstream function**. Its own
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
automatic retention and cancellation are not included in v0.1.

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
