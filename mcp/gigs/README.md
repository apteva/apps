# Gigs (v0.1.26)

Agents delegate atomic work to human workers (CRM contacts) by composing
reusable multi-modal instructions. Templates are saved instruction sets;
gigs are dispatched snapshots. Async by default — agents resume when the
worker submits.

## Three layers

1. **Instruction library** — atomic, versioned units. The dashboard creation
   flow currently exposes text, audio, and video instructions.
2. **Templates** — ordered compositions of pinned instruction versions
   with title + defaults + per-use overrides.
3. **Gigs** — immutable snapshots, composed at dispatch from a template +
   vars (or from instructions directly, or fully inline).

The dashboard supports the same core workflow: create instructions, build
template compositions, dispatch gigs from templates or selected instructions,
assign workers, and review submitted results.

Text, audio, and video instructions are read-only by default. Set
`body.response_mode` to `optional` or `required` when a worker should add
notes/files for that step; otherwise the worker page only shows the instruction.

`result_schema`, `media_manifest`, `checklist`, and `variables` are
**derived** from the composition — never hand-authored.

## Rescheduling a gig

`gigs_extend_deadline` moves `deadline_at` and nothing else. If the title or
`vars` mention the original date, follow it with `gigs_update` so the record
does not contradict itself:

```
gigs_extend_deadline  id=11  deadline_at=2026-08-20T22:00:00+03:00
gigs_update           id=11  title="Veronika — Aug 20 recording"  vars={"recording_date":"2026-08-20"}
```

This matters because the title is worker-facing — `handleWorkerGigJSON` serves
it to the gig page the worker opens from their link, so a stale date (or an
internal codename) is shown to them.

`vars` is a patch: supplied keys win, untouched keys survive, an explicit
`null` drops a key. `gigs_update` is refused once a gig is `reviewed`,
`cancelled`, or `expired`, and it does **not** re-render an already dispatched
composition — that snapshot is frozen at dispatch by design.

## Home widget

`gig-queue` (`ui/GigQueueWidget.tsx`) is a `dashboard.home` widget: submissions
awaiting review, work in flight with deadline urgency pills, and optionally
recent outcomes. Half size shows counts plus the three most urgent rows; full
size shows each enabled section. It refreshes live on the app's `gig.*` events
and reads the same `/gigs` summaries the panel uses — no widget-specific
backend. Sections and the recent-outcome limit are per-instance settings.

## Deleting a gig

`gigs_cancel` is the normal way to end work — the gig stays in the record as
`cancelled`. `gigs_delete` is for gigs that should never have existed, and it
is genuinely destructive:

- Removes the gig plus its `gig_instructions`, `gig_assignments`,
  `gig_submissions`, `gig_events` and `gig_upload_sessions` rows, in one
  transaction. There is no archive and no undo.
- Finished gigs (`reviewed`, `cancelled`, `expired`) delete directly. A live
  gig needs `force=true`, which also strands the assigned worker's magic link.
- **Storage files are left in place.** Instruction media belongs to the
  instruction library and outlives any single gig, and storage deduplicates,
  so a file this gig referenced may still be referenced elsewhere.
- Returns `{deleted, gig}` — per-table row counts plus the gig record as it
  was, since the caller loses it otherwise.

Children are deleted explicitly rather than relying on `ON DELETE CASCADE`, so
the result does not depend on the `foreign_keys` pragma being live.

## Gig status vocabulary

A gig's `status` is exactly one of:

| Status | Meaning |
|---|---|
| `open` | Dispatched, nobody offered it yet |
| `offered` | Offered to one or more workers, not yet accepted |
| `accepted` | A worker took it; work in progress |
| `submitted` | Worker submitted; awaiting agent review |
| `reviewed` | **Terminal.** Submission accepted via `gigs_accept_result` |
| `cancelled` | **Terminal.** Cancelled via `gigs_cancel` |
| `expired` | **Terminal.** Deadline elapsed without an accepted submission |

**There is no `completed` status.** Work that was accepted is `reviewed`.
`gigs_list_open` accepts `completed`, `complete`, and `done` as aliases for
`reviewed` so the natural phrasing works, and rejects any other unknown value
with an error rather than returning an empty list.

`completed_at` is stamped on `reviewed` **and** `expired`, so it marks "reached
a terminal state", not "succeeded" — filter on `status` when you mean success.

`rejected` is not a gig status: rejecting a submission records it on the
assignment and as a `gig_event`, and returns the gig to an earlier status
(`submitted`, `accepted`, `offered`, or `open`).

## Hard deps

- `crm` (required) — workers are CRM contacts; notifications and timeline
  logging go through `crm.contacts_send_message` / `contacts_log_activity`.
- `storage` (required) — audio/video instruction media and worker submissions live
  under `/.gigs/` (configurable).
- `domains` (optional) — publishes branded worker-link hostnames through
  Domains-managed DNS and server-native ingress. No CDN app is required.

## Public worker links

The Public links panel can attach multiple hostnames from the Domains inventory
and select one default. New assignments snapshot that hostname, so links already
sent to workers do not change when the default changes. Existing gig creation and
assignment tools may pass `public_domain_id` to select a non-default hostname.

## Worker flow

1. Agent: `gigs_create_from_template(slug, vars, worker_id?)`.
2. Sidecar resolves the worker → contact, renders the composition with
   `vars` interpolated, copies it into the gig snapshot, mints a
   `magic_token`, and optionally notifies via CRM.
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
