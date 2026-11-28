# How to use Workspaces

Workspaces answers where work runs, who owns it, what command is active, and
when the environment will disappear. It is not a code editor or Git client.

## Where a workspace runs

Each workspace has a `driver`, shown on the workspace with its `capabilities`:

- `local`: a folder on the machine running Apteva. Commands run in the OS
  sandbox (Seatbelt on macOS, bubblewrap on Linux), or, when Apteva itself
  runs in a container, as the workspace's own unprivileged user
  (`capabilities.sandbox` says which), with the machine's own
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

## Install what you need

A workspace is a general-purpose machine: install the tools a task needs
instead of giving up when one is missing. `capabilities.system_packages` says
how:

- `pkg` (local workspace, container mode): run `pkg add <names>` as an
  ordinary command, for example `pkg add nodejs npm`, `pkg add python3
  py3-pip`, `pkg add git build-base`, `pkg add go`. Names are Alpine packages;
  `pkg search <term>` finds them, `pkg list` shows what this workspace added,
  `pkg del <name>` removes one. Packages are shared by every workspace on the
  machine and come back by themselves after a restart. Commands still run as
  the workspace's own user, not root.
- `root` (Docker workspace): commands run as root in the image, so use its
  package manager directly (`apt-get update && apt-get install -y …` on the
  Debian-based profile images, `apk add …` on Alpine images).
- empty (local workspace under the macOS or Linux OS sandbox): system packages
  cannot be installed; use what the machine has, or create a Docker workspace.

Language packages need no root anywhere: `npm install` (and `npm install -g`,
which goes to `~/.local`), `pip install` inside a virtualenv (`python3 -m venv
.venv`), `go install` (to `~/go/bin`) and `bun add` all write to the
workspace, and those bin folders are on `PATH`.

## Processes

A command may leave a server or watcher running. `workspace_processes` lists
the processes of a local workspace (pid, parent, memory, run time, command);
pass `kill` with a pid, or `kill_all`, with an optional `signal` (default
TERM), to stop them. In a Docker workspace use `ps aux` and `kill` as commands.

## Preview a web app

Start the server with `workspace_preview_start` (`argv` such as
`["bun","run","dev","--host","0.0.0.0","--port","3000"]`). Local workspaces
take any `port` (default 3000) and the server may bind `127.0.0.1` or
`0.0.0.0`. A Docker workspace publishes its port when it is created, so create
it with `preview_port` and bind `0.0.0.0`; one created without it cannot
preview, so create a new one. Poll `workspace_preview_get` until `status` is
`live`. Its `url` is a link through Apteva that anyone who has it can open,
no Apteva sign-in needed; share it instead of `local_url` (the address on the
Apteva machine). The link changes each time the preview starts. The app is
served under that path prefix, so prefer relative asset links, or read the
`X-Forwarded-Prefix` request header. Pages run sandboxed in an opaque origin:
scripts, forms, websockets and simple fetches to the app's own relative URLs
(GET, or POST with a form or `text/plain` body) work. Cookies and
`localStorage` do not, and requests that need a CORS preflight (a JSON
`Content-Type` or custom headers) are refused through the link for now, so
keep demo state in memory or on the server, and send JSON as `text/plain`
when a preview must post it.
`workspace_preview_logs` shows server output. `workspace_preview_stop` ends the server and keeps the workspace
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
