# How to use Workspaces

Workspaces answers where work runs, who owns it, what command is active, and
when the environment will disappear. It is not a code editor or Git client.

## Where a workspace runs

Each workspace has a `driver`, shown on the workspace with its `capabilities`:

- `local`: a folder on the machine running Apteva. Commands run in the OS
  sandbox (Seatbelt on macOS, bubblewrap on Linux) with the machine's own
  toolchains (Go, Bun, Python, Node, Git, whatever is installed). No other app
  is needed. Each command runs on its own, so `cd` and exported variables do
  not carry over; use `working_directory`. Profiles and `image` do not apply.
- `containers`: a Docker container with a persistent PTY shell, approved
  profile images and optional Instances hosts. Needs the Containers app.

Omit `driver` and Workspaces picks: Docker when Containers is installed,
otherwise `local`. You do not need to install Containers to use Workspaces.
Only install it when you need Docker images, a persistent shell, Instances
hosts or test workspaces. If a create fails saying Containers is missing, retry
with `driver: "local"` instead of installing it.

## Start a workspace

Use `workspaces_create` with a short recognizable name, a concrete purpose,
the closest approved profile (`go`, `bun`, or `python`, used by Docker
workspaces), and a realistic TTL.
The optional `apteva` profile works only when an operator configured its
combined development image.

When a profile image is not suitable, pass `image` with an operator-allowlisted
container reference. Custom images must use an immutable `@sha256` digest by
default. The profile still describes the expected toolchain; the explicit image
only overrides what Containers runs.

Keep the returned `workspace.id`; every later operation uses it. If another app
created the workspace, respect the repository/branch labels and origin link as
context. Those labels are not proof of the current Git state.

## Run commands

Call `workspaces_get` before acting so you know the current lifecycle and active
command. Only a `running` workspace accepts commands and only one command can
be active at a time.

Prefer an `argv` array:

```json
{"workspace_id":"wsp_…","argv":["go","test","./..."]}
```

Use `shell_command` only when shell syntax such as pipes, `cd`, or `export`
is actually required. Never place secrets in commands. Workspaces intentionally
does not accept arbitrary environment values.

In a Docker workspace, commands run through one persistent PTY shell inside
the workspace container (`capabilities.sessions` is true).
Current directory, exported shell variables, functions, aliases, installed
packages, writable-layer files, `/workspace`, and `/cache` persist across
commands. Omit `working_directory` to continue from the shell's current
directory; when supplied, it must remain under `/workspace`. Background services
also persist while the container runs. Stopping the workspace terminates its
processes and shell; resume starts a fresh shell in the preserved filesystem.
In a local workspace, `/workspace` and `/cache` map to folders on the machine
and each command starts fresh in `working_directory`.

After `workspace_command_start`, call `workspace_command_wait` instead of
polling. It blocks up to `wait_ms` (default 15 s, max 20 s) and returns the
command status, the output produced since your `cursor`, and the next `cursor`.
Call it again with that cursor until the status is terminal: `succeeded`,
`failed`, `cancelled`, or `timed_out`. Output currently becomes available when
a command finishes, so an empty `output` while it is `running` is normal; keep
waiting rather than restarting the command. For long-running servers use a
preview, whose logs are readable while it runs. `workspace_command_output` returns new
output without waiting; `gap=true` means older output scrolled out of the
64 KB window. If a running command is no longer useful, cancel it before
starting another.

## Work with files

Use the file tools for files and keep the shell for running programs. Do not
write files with heredocs, `echo >`, or `sed -i`.

- `workspace_file_list` shows a directory tree (`path`, `max_depth`,
  `name_pattern` such as `*.go`). It prunes `.git`, `node_modules` and `.home`
  unless you pass `exclude`.
- `workspace_file_read` returns a line window (`offset`, `limit`, 1-based) plus
  `total_lines` and the whole file's `sha256`. Text files up to 2 MB only; use
  `head`, `tail` or `grep` through a command for logs and larger files.
- `workspace_apply_edits` replaces exact strings. Copy `old_string` from what
  you read, including indentation, with enough context to match once (or set
  `replace_all`). Pass the `sha256` from your read as `expected_sha256` so the
  edit fails instead of clobbering a file that changed since.
- `workspace_file_write` creates or replaces a whole file and creates parent
  directories. Use `create_only=true` for new files, and `expected_sha256` when
  replacing one you read.
- `workspace_source_import` unpacks a tar.gz (max 8 MB) into a directory when
  no command is running.

Paths are relative to `/workspace`; `/workspace/...` is also accepted. Files
outside `/workspace` (system files, `/cache`) are reachable only through
commands. Reads and writes briefly pause the container while the volume is
transferred.

Use `grep -rn` or `rg` through a command to search file contents.

## Preview a web app

Create the workspace with `preview_port` (the port your server will listen on)
to publish it on the host. Start the server with `workspace_preview_start`
(`argv` such as `["bun","run","dev","--host","0.0.0.0"]`); it must bind
`0.0.0.0`. Poll `workspace_preview_get` until `status` is `live`, then use its
`url` (a loopback address on the Apteva host). `workspace_preview_logs` shows
server output. `workspace_preview_stop` ends the server and keeps the workspace
running; pass `keep_workspace_running=false` to stop the container too. A
preview runs outside your shell session, so it does not block commands.

## Lifecycle

- `workspace_stop` cancels active work, stops the supervisor workload, and
  preserves both volumes.
- `workspace_resume` starts a suspended workspace only while its TTL is valid.
- `workspace_extend` sets a fresh TTL relative to now. Extend an expired
  workspace before resuming it.
- At expiry, Workspaces cancels work and stops the workload. It retains volumes
  until `delete_at`, then deletes them automatically.
- `workspace_destroy` permanently deletes the workload and its volumes. Pass
  `confirm=true` only after checking the origin app or exporting the source.

Workspaces does not inspect Git. Treat `dirty_state=unknown` or
`unpushed_state=unknown` as a real data-loss risk, not as clean state.

## Source owned by another app

Source synchronization tools are app-only. The originating app supplies a
digest and managed path list when creating the workspace, uses
`workspace_source_sync` with the expected prior digest for later refreshes, and
uses `workspace_source_export` with `managed=true` to retrieve source changes.
Only that same app installation can perform these operations.

After the originating app safely reconciles an export, it calls
`workspace_source_accept`. Workspaces records that revision but never treats it
as a Git commit and never pushes it. Humans and agents should use the
originating app—in particular Code—for repository reconciliation.
