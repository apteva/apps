# Browser test protocol v1 (0.9.0)

Computer exposes four tools for bound Tests installations:
`browser_test_capabilities`, `browser_test_session`, `browser_test_step` and
`browser_test_capture`. Calls require a trusted caller app/install/project
identity; human and unrelated-app calls are rejected. They use
`apteva-browser-test/v1` and run local Chromium independently of saved Computer
sessions, accounts and user profiles. See [Tests](../tests/README.md#browser-checks-030)
for suite syntax.

Chrome or Chromium must be installed on the Computer host; set `CHROME_BIN` to
select its binary. The executable is checked before queuing tests. A SQLite
partial unique index reserves one starting/active test browser across projects.
Tests waits within its check deadline when that slot is busy. Profiles are
created under the install's data directory and deleted on close. Leases last
10–300 seconds; restart reconciliation terminates only processes matching an
owned unique profile argument before releasing an interrupted slot.

The browser is launched with a fixed loopback forward proxy, loopback proxy
bypass disabled, QUIC disabled and nonproxied WebRTC UDP disabled. Only exact
public route prefixes of named HTTP runtime targets are forwarded. CONNECT,
foreign hosts, management paths, other runtimes and path traversal are denied.
Background Chromium requests also remain blocked. Page-origin denied attempts
are distinguished from browser background traffic for check verdicts. Upstream
HTTP 403 responses are observations, not network-boundary violations.

Each check receives a fresh profile, fixed viewport/locale/timezone, strict
locators and serialized actions. Tests polls observations/readiness and sends
mutating actions once. Supported inputs reuse Computer's existing guarded
text, select, checked-state and fixture-upload primitives. Screenshots mask
input/contenteditable fields, marked sensitive elements and registered secrets;
PNG bytes are returned immediately for Tests-owned storage. No recordings are
produced. Structured diagnostics exclude headers, bodies, queries and values.

For `command.action=select`, `command.value` must be an explicit string.
`"value": ""` selects the option with an exactly empty value; omitted or null
selection values fail before input dispatch. Exact matching, disabled-option
checks, ambiguity rejection and application-reconciliation checks still apply.
Non-sensitive observations retain `"value": ""` after an empty selection.
For `fill`, explicit empty text still clears the field, as does its legacy
omitted value; sensitive values remain redacted.

```sh
GOWORK=off go test -race ./...
RUN_BROWSER_TESTS=1 GOWORK=off go test -race -count=1 ./internal/browser/testbrowser
```

The real-browser test drives text, password, selection, checkbox, upload and
click inputs, checks fresh storage, captures a masked PNG and cancels a command.
It attempts foreign fetches, management/other-runtime URLs, frames, images,
WebSockets, service workers and redirects and verifies none reach the servers.

If interrupted test-browser recovery fails, Computer keeps its existing tools
available and reports browser testing as unavailable. Test session actions fail
closed until the recovery problem is corrected and the app restarts. Pending
leases remain reserved; saved contexts and normal sessions are unaffected.
