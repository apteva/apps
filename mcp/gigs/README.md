# Gigs (v0.1)

Agents delegate atomic work to human workers (CRM contacts) by composing
reusable multi-modal instructions. Templates are saved instruction sets;
gigs are dispatched snapshots. Async by default — agents resume when the
worker submits.

## Three layers

1. **Instruction library** — atomic, versioned, multi-modal units (text,
   audio, video, image, document, link, script, warning, example,
   checklist_item, confirmation, timer_hint, input_*).
2. **Templates** — ordered compositions of pinned instruction versions
   with title + defaults + per-use overrides.
3. **Gigs** — immutable snapshots, composed at dispatch from a template +
   vars (or from instructions directly, or fully inline).

`result_schema`, `media_manifest`, `checklist`, and `variables` are
**derived** from the composition — never hand-authored.

## Hard deps

- `crm` (required) — workers are CRM contacts; notifications and timeline
  logging go through `crm.contacts_send_message` / `contacts_log_activity`.
- `storage` (required) — instruction media and worker submissions live
  under `/.gigs/` (configurable).

## Worker flow

1. Agent: `gigs_create_from_template(slug, vars, worker_id?)`.
2. Sidecar resolves the worker → contact, renders the composition with
   `vars` interpolated, copies it into the gig snapshot, mints a
   `magic_token`, and notifies via CRM.
3. Worker opens `/worker/<token>` → reads instructions in order, ticks
   the checklist, fills the form, uploads attachments → submit.
4. Sidecar validates against `derived_result_schema_json`, writes a
   submission row, emits `gig.submitted`. The agent's waiting branch
   wakes.

Lightweight gigs (yes/no, short text) also accept thread replies via the
`crm.contact.message_received` event handler.

## Local development

```bash
cd mcp/gigs
go build .
APTEVA_PROJECT_ID=test ./gigs           # smoke run; binds to :8080
curl http://localhost:8080/health
```

See `migrations/001_init.sql` for the schema. Each Go file is one
surface: `workers.go`, `instructions.go`, `templates.go`, `gigs.go`,
`worker_page.go`, `composition.go` (derivation), `crm.go` /
`storage.go` (inter-app helpers).
