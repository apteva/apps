# Processes 0.21.1

Better trigger ranking when the agent gives no query.

In 0.21.0 `trigger_sources` counted any word the procedure text shared with an
event's name or description. Long procedure prose ("figures are not changed
before sending") pushed unrelated events up: on a real project the CRM Stats
procedure ranked `opportunity.stage.changed` first.

- Procedure text now counts only words that name the app, the install or the
  event subject (crm, contact, ticket), never event verbs such as added,
  changed or created, and never description prose.
- The agent's query still matches names and verbs fully; a query word found
  only in an event description counts a quarter.
- Ties go to the most specific event: for "a contact is added",
  `contact.added` ranks above `contact.activity.added`.
- Reasons now say "procedure names: …".

Replayed against a real local project (54 sources, 486 events): with no query
the CRM procedure's top candidates are CRM contact and conversation events with
no prose matches; "a new contact is added to the CRM" puts `contact.added`
first on both CRM installs; "a ticket is created" puts `ticket.created` first.

Validated with `GOWORK=off go test -race ./...` (one new test), the scenario
Bun tests and Tier 3 `20-trigger-discovery` on Codex `gpt-6.1-terra`. No API
shape, UI, migration, Core, Server or SDK changes.
