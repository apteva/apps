# How to use Workspaces

Workspaces answers where work runs, who owns it, what command is active, and
when the environment will disappear. Each workspace is one persistent Docker
container with a persistent PTY shell. It is not a code editor or Git client.

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
{"workspace_id":"wsp_…","argv":["go","test","./..."]}
```

Use `shell_command` only when shell syntax such as pipes or redirection is
actually required. Never place secrets in commands. Workspaces intentionally
does not accept arbitrary environment values.

Commands run through one persistent PTY shell inside the workspace container.
Current directory, exported shell variables, functions, aliases, installed
packages, writable-layer files, `/workspace`, and `/cache` persist across
commands. Omit `working_directory` to continue from the shell's current
directory; when supplied, it must remain under `/workspace`. Background services
also persist while the container runs. Stopping the workspace terminates its
processes and shell; resume starts a fresh shell in the preserved filesystem.

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
