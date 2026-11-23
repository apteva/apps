# Workflows

Deterministic, auditable pipelines. A workflow is a YAML/JSON graph of
typed steps (`http`, `function`, `app`, `integration`, `emit`, `branch`)
triggered manually, over HTTP (`/wf/<name>`), or by another app's event
stream. Every run records a per-step trace you can inspect and replay.
See [`examples/`](examples/) for ready-to-deploy definitions.

## Stages (0.5.0)

Keep **one** workflow per job and release it through named stages
instead of creating a new workflow per release.

- **Frozen versions.** Every definition change (create, or an update
  that touches the source) stores an immutable version. `workflows_get`
  lists them; `workflows_get {name, version}` returns one version's
  source.
- **Stages** are free-text names (`^[a-z0-9][a-z0-9-]{0,39}$`, e.g.
  `staging`, `production`) that point at a version and carry their own
  `vars`, an optional `filter` and a status (`active` / `paused`).
  Editing a workflow never moves a stage.
- **No stages = unchanged behaviour.** Events, HTTP and manual runs
  execute the live head definition, exactly as before 0.5.0.

Release flow:

```text
workflows_update {name: "sync", source: "..."}         # -> version 4, not live anywhere
workflows_stage_promote_many {stage: "staging",
  items: [{workflow: "sync", version: 4}]}             # validate + point staging at v4
workflows_run {name: "sync", stage: "staging", input: {...}}   # test
workflows_stage_promote_many {stage: "production",
  items: [{workflow: "sync", version: 4, expected_rev: 3}]}
workflows_stage_rollback {stage: "production", workflows: [{workflow: "sync", expected_rev: 4}]}
```

Tools:

| Tool | Args |
|---|---|
| `workflows_stage_set` | `workflow, stage, version?` (required to create), `vars?, filter?, status?, expected_rev?` |
| `workflows_stage_list` | `workflow?, stage?` |
| `workflows_stage_delete` | `workflow, stage, expected_rev?` (no-op if absent) |
| `workflows_stage_promote_many` | `stage, items: [{workflow, version, expected_rev?}]` — one transaction |
| `workflows_stage_rollback` | `stage, workflows: [id \| name \| {workflow, expected_rev?}]` — one transaction |

Every move validates the target version with the definition validator.
`expected_rev` is an optimistic lock (`0` = stage must not exist yet);
a mismatch fails the whole call and changes nothing. Repeating a call
with the same values is a no-op (rev and `previous_version` unchanged).
Rollback swaps `version` and `previous_version`, so pass `expected_rev`
when retrying it.

**Event dispatch.** For a workflow with stages, each event starts one
run per *active* stage whose version's own trigger (source, topic,
`when`) matches **and** whose stage `filter` matches. Paused stages
are skipped (they can still be run by hand); if every stage is paused
nothing runs. Filters run before any run row is created:

```json
{"when": "input.data.env == 'staging'", "source_install_id": 42}
```

`when` uses the trigger-predicate grammar (paths `input.*`, `stage.*`).
`source_install_id` matches the publishing installation: the frame's
`source_install_id`, else the `install_id` the platform stamps on every
AppBus event (0.5.1). Frames without either (or with `install_id` 0, from
older platforms) are not filtered by it.

**Templates.** Step inputs and conditions can use `{{ stage.name }}`,
`{{ stage.vars.<key> }}` and `{{ stage.version }}`. They resolve to
nothing for unstaged runs.

**Function steps on a stage (0.5.2).** A `kind: function` step accepts
`stage:` (templated like input), passed to `functions_invoke` as its
`stage`, e.g. `stage: "{{ stage.vars.functions_stage }}"`. Empty after
rendering runs the function's active version; an invalid name fails the
step without calling Functions.

**Runs** record `stage` and `workflow_version`; `workflows_runs` and
`GET /workflows/<id>/runs` accept `stage` to filter. `workflows_run`
and `/wf/<name>?stage=` run a stage's version with its vars; without a
stage they run the head. Replay re-runs the original stage, version
and stage vars, whatever the stage points at now.
