# Run chain check — 2026-09-14

## Confirmed preview failure

The local Code v0.12.0 log showed `npm run dev` starting Vite v8.3.0
successfully on localhost:5173. Code had allocated 6100, exported PORT=6100,
and probed 127.0.0.1:6100. Vite ignores that environment variable. The startup
watchdog eventually terminated the otherwise healthy Vite process.

Code v0.12.1 passes `--host 127.0.0.1 --port <allocated> --strictPort` to direct
Vite dev/start scripts, with the package manager's argument forwarding syntax.
Custom commands retain their explicit PORT contract. Early environment and
dependency-plan errors now mark the run crashed rather than leaving it starting.

## Live chain validation

A disposable Code repository containing a copy of the reported React source
was exercised through the actual platform MCP gateway and installed bindings:
Code 0.12.0 → Workspaces 0.5.0 → Containers 0.4.0 → local Docker.

- Source import, dependency installation and Vite production build succeeded.
- A command exiting 7 returned failure with exit_code=7 and its logs.
- A one-second deadline cancelled a sleeping command; the next command succeeded.
- A generated source file was previewed, applied by exact workspace digest,
  and read back through Code with the expected content.
- Workspace destruction and repository cleanup succeeded. The workload and
  workspace records both ended destroyed.

Run previews currently take Code's local dev path. Workspaces provisions no
published ports and exposes no preview start/stop/URL contract. Its command
API is suitable for finite builds and tests; a Workspaces binding alone does
not provide a browser preview. No fallback to unapproved local execution occurs.

The installed Containers 0.4.0 emitted a stale APTEVA_END shell control marker
in the first command log after cancellation. Recovery and command results were
correct; this log-format issue is separate from the Code preview failure and
is not fixed by the Code port patch. The current Containers 0.5.0 suite passed,
but that does not establish that this intermittent log-format issue is resolved.

## Patch validation

The real-Vite opt-in test in dev_vite_test.go verifies HTTP readiness at Code's
allocated port (even with an existing --port 5173 script argument) and Stop.
Unit cases cover npm/Bun/pnpm/Yarn forwarding and custom scripts. Code race and
integration tests, TypeScript, ten browser tests, vet and Linux compilation
are release checks. Workspaces race tests and Containers race tests including
real Docker persistent execution tests also pass.
