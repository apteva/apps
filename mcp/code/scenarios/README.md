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

## Adding a scenario

Each scenario is one self-contained YAML with `directive` (the
prompt), `assert` (post-conditions checked against tool calls and
HTTP), and `budget` (token + cost ceiling). Keep one capability per
file — combos make failures hard to diagnose.
