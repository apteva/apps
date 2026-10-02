# Code scenarios

Tier 3 live-agent tests. Each YAML is one scenario the runner
installs the local Code build, gives the agent the directive,
watches telemetry, then runs assertions against the running
sidecar's REST surface.

## Run

```bash
# Run every scenario.
apteva test ./scenarios/

# One scenario, verbose.
apteva test ./scenarios/01-create-from-template.yaml -v
```

## What's covered

| File | Exercises |
|---|---|
| `01-create-from-template.yaml` | `repos_create` + `code_list_files` — template materialisation |
| `02-write-and-read.yaml` | `code_write_file` + `code_read_file` — agent-generated content round-trip |
| `03-edit-with-uniqueness.yaml` | `code_edit_file` with non-unique target → must include context |
| `04-grep-then-edit.yaml` | `code_grep` → `code_edit_file` — the realistic find-then-fix loop |
| `05-multi-edit-refactor.yaml` | `code_multi_edit` — atomic batched edits |
| `06-workspace-round-trip.yaml` | Real Containers + Workspaces execution, revision preview, exact-digest apply, and source verification |
| `10-native-html-preview.yaml` | Blank native repo, one-file HTML editing and live workspace preview, with no Git mutations or checkpoint; verifies saved source and preview state |

## Adding a scenario

Each scenario is one self-contained YAML with `directive` (the
prompt), `assert` (post-conditions checked against tool calls and
HTTP), and `budget` (token + cost ceiling). Keep one capability per
file — combos make failures hard to diagnose.

Scenarios that exercise optional app dependencies opt in explicitly with
`setup.app.bindings.<name>: app`. Use `setup.cleanup_mcp_calls` for durable
resources that must be removed even when an assertion fails.

## Native HTML preview regression

`10-native-html-preview.yaml` uses a normal local-coding request rather than
telling the agent which preview tools to choose or hiding the Git tools.
It requires two successful live checks, no failed editing/preview calls, no
Git mutations or checkpoint, and verifies saved repository and preview state.
The runner stops the temporary preview and removes its test repository during
cleanup; the returned test URL is not intended to stay available afterward.

With owner authentication configured for the local test server:

```bash
apteva test --server 127.0.0.1:5280 --provider openai-codex --model gpt-6.1-sol scenarios/10-native-html-preview.yaml
```

Validated on 2026-10-02 against the local Code build and exported skill with
real Workspaces and Containers: all assertions passed in 11 iterations. All
observed tool calls succeeded; no Git mutation or native checkpoint occurred.
