# Processes 0.21.0

Agents can find which app event can start an assignment.

## Trigger discovery

`processes_trigger_sources` takes optional `process_id` + `assignment_id`,
`app` and `query`. It is the same tool, so agents still see 42 tools.

- With an assignment it returns up to 25 ranked candidates: the source install,
  topic, description and documented fields, `suggested_mappings` (parameter to
  `data.<field>`), covered parameters, `missing_required`, `ready`, the reasons
  for the rank, and `other_installs_of_app` when an app is installed twice.
- Ranking: documented event fields are matched to the assignment's parameters
  (exact key, all key words in the field such as `lead_email` for `email`, or a
  shared word with the key or label; types must be compatible). Words from the
  agent's `query` count most, words from the procedure's own text a little.
  Events that fill every required parameter come first. Events with no field
  docs appear only when the query or procedure names them, flagged for preview.
- Without an assignment it lists each app's event names (sources with no events
  are skipped); `app=<name>` also returns that app's event fields, and `query`
  filters events.
- The HTTP route `GET /processes/<id>/trigger-sources` accepts the same
  `assignment_id`, `app` and `query` parameters.

## Checks

- `trigger_create` / `trigger_update` fail when the source install does not
  publish the topic (the error lists what it does publish) or is not an event
  source in the project. Dynamic `x.*` topics still match.
- The saved trigger carries `warnings` for mappings to fields the event does not
  document and for required parameters nothing fills.
- `trigger_status active` is refused while a required parameter has no mapping,
  assignment value or default.

If the platform cannot list event sources, triggers are saved unchecked with a
warning, as before.

## Docs and tests

- The agent skill has an Event triggers section: find, create paused, preview,
  activate.
- Three new Go tests (ranking, listing, source and activation checks).
- New core Tier 3 scenario `20-trigger-discovery` with a sales-pipeline fixture
  app (four events) and the signup fixture as a decoy. The agent is only told
  "start Sales whenever a lead becomes qualified with a score of at least 80".
  On Codex `gpt-6.1-terra` it found `lead.qualified` through the ranked lookup,
  used the suggested mappings and a `data.score >= 80` filter; the score-41 lead
  was skipped and the score-92 lead started the one completed run (16
  iterations, 348k tokens). `05-event-trigger-workflow` still passes.

No UI, migration, Core, Server or SDK changes.
