# How to use Workspaces

Workspaces answers where work runs, who owns it, what command is active, and
when the environment will disappear. Each workspace is one persistent Docker
container. It is not a code editor, Git client, or interactive terminal.

## Start a workspace

Use `workspaces_create` with a short recognizable name, a concrete purpose,
the closest approved profile (`go`, `bun`, or `python`), and a realistic TTL.
The optional `apteva` profile works only when an operator configured its
combined development image.

Keep the returned `workspace.id`; every later operation uses it. If another app
created the workspace, respect the repository/branch labels and origin link as
context. Those labels are not proof of the current Git state.

## Run commands

Call `workspaces_get` before acting so you know the current lifecycle and active
command. Only a `running` workspace accepts commands and only one command can
be active at a time.

Prefer an `argv` array:

```json
{"workspace_id":"wsp_…","argv":["go","test","./..."],"working_directory":"/workspace"}
```

Use `shell_command` only when shell syntax such as pipes or redirection is
actually required. Never place secrets in commands. Workspaces intentionally
does not accept arbitrary environment values.

Every command starts as a process inside the same workspace container.
Installed packages, writable-layer files, `/workspace`, and `/cache` persist
across commands and stop/resume. Background services persist between commands
while the container runs; stopping the workspace terminates processes. A
discrete command still gets a fresh shell process, so `cd` and exported shell
variables do not carry to the next command. Set `working_directory` on each
call; it must remain under `/workspace`.

Use `workspace_command_get` for status and `workspace_command_logs` for bounded
output. Do not poll in a tight loop. Terminal states are `succeeded`, `failed`,
`cancelled`, and `timed_out`. If a running command is no longer useful, cancel
it before starting another.

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
