# Operator-authorized scheduling

A schedule-only workflow can prevent a fully confirmed immediate Publish click.
Computer stores the constraint independently of session state and click arguments.
The operator or a trusted task controller must register the authorized resource
and time before the agent can commit it. Computer does not derive authorization
from task prose, and existing workflows without a registered constraint keep the
existing consequence guard. Deploy both the Computer change and the corresponding
server operator-identity change before using this API.

## Register the authorized schedule

An authenticated operator calls `POST /apps/computer/workflow-constraints` through
the Apteva server, using the appropriate install/project routing. The sidecar
route is `/workflow-constraints`. Use an ordinary operator session or operator API
key; keep that credential outside agent tools and browser contexts.

Example body (replace the resource, saved context and future instant):

```json
{
  "id": "task-123-post-456",
  "context_id": "ctx_saved_creator",
  "resource_url": "https://example.com/posts/456/edit",
  "allowed_effect": "scheduled_external_commit",
  "scheduled_at": "2027-09-22T19:00:00-07:00",
  "timezone": "America/Los_Angeles",
  "date_selector": "input[type=date]",
  "time_selector": "input[type=time]"
}
```

The resource URL must be exact, without query or fragment. Save and reload a new
draft before registering it when the site canonicalizes its URL (for example,
by adding the title slug). Verify its immutable post identity across that reload.
Changing the title/URL later requires the operator to review and replace the
resource constraint; Computer does not silently widen it. The time is a future
RFC3339 instant at minute precision. The selectors must each resolve to one
enabled native date/time input. The operator supplies them, not the model.
This verifier supports browser-local native scheduling controls: it checks the
browser IANA timezone and the control values against the authorized instant.
It does not establish the meaning of an unrelated site/account timezone setting.

There is one active workflow per saved context. Registration cannot overwrite it
and returns 409 for a duplicate. It survives session closure/reopening and sidecar
restarts. It applies throughout that context and to another context currently on
the exact protected resource. A commit on a different resource is rejected.
After the workflow is complete, an operator can explicitly release it with
`DELETE /apps/computer/workflow-constraints/{id}`. It never silently expires or
widens itself, and ordinary clicks cannot replace or remove it.

## Enforcement and trust boundary

The server strips caller-supplied `X-Apteva-Operator-ID` and mints it only from
verified operator session/API-key authentication. Agent gateways, environment
app gateways, delegated app users and bound sibling-app calls cannot mint it.
The sidecar requires its SDK bearer boundary as well as this operator identity.
Do not expose the sidecar token to agents. An old server cannot safely provide
this boundary; upgrade the server together with Computer.

Every `computer_use` action, including each batch step, reloads persisted policy.
Local and Browserbase guarded clicks compare the live consequence with the
allowed workflow effect before considering model-supplied acknowledgements.
Scheduled commits also require the exact resource, date, time and timezone.
Stale, disabled, loading and consequence checks remain in force. Enter/Space
activation and typed newlines cannot bypass the click guard. Unsupported backends
fail closed while a constraint applies. No Patreon-specific production logic is
used; consequence recognition retains the existing semantic detector's coverage.

Observations include read-only `workflow_constraints` summaries, including
`authority: operator` and `mutable_by_agent: false`. Rejections explain the
allowed effect and time without presenting a ready-to-copy dangerous confirmation
as the way to continue. The model can correct the date/time and choose Schedule;
it cannot authorize immediate publication by repeating confirmation fields.

## Coverage

- Unit tests: operator identity, persistence, context/resource scope, keyboard
  bypass, live effect, exact resource/time/zone, and expired/unverifiable policy.
- Real local browser: label, stable target, selector, coordinates, double click,
  batch, reopened session; no immediate HTTP commit, one authorized schedule.
- Tier 3 saved Patreon/Browserbase context: forced fully confirmed wrong Publish,
  persistence after reopen, forced wrong-time Schedule, model recovery, final
  scheduled state and original post/title verified after reload.

This is enforcement for registered workflows, not automatic protection for every
natural-language scheduling task. A trusted task integration must supply the
record before production scheduling work; a model-generated record would not
provide independent authorization.
