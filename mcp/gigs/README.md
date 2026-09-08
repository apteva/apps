# Gigs (v0.5.2)

Gigs is a generic work marketplace and execution engine. Agents can define
standard services and packages, know the usual customer offer and worker pay,
negotiate open jobs into contracts, and dispatch immutable work snapshots to
human workers (CRM contacts). Async execution remains the core: agents resume
when the worker submits.

## Commercial model

The commercial layer is deliberately split from execution:

1. **Pay grades** classify compensation independently from skill proficiency.
   A worker has one grade and may also have worker-specific overrides.
2. **Rate cards** define effective worker pay by worker or grade, optionally
   narrowed to a template or offer package. Resolution is deterministic:
   worker+package, worker+template, package+grade, template+grade, general
   grade rate, then the grade default.
3. **Standard offers** wrap a published Gigs template in Basic, Standard,
   Premium, or custom packages. Each package captures scope, delivery,
   revisions, sell price, and pricing model.
4. **Contracts and milestones** snapshot accepted customer and worker terms.
   They may come from a package, direct negotiation, an order, a subscription,
   or an accepted worker proposal.
5. **Gig compensation** is frozen when work is dispatched or assigned. Later
   rate changes never rewrite agreed pay. The worker link shows that amount.

`offers_recommend` is the agent-facing preflight: it returns the matched
standard offer/package, customer price, resolved worker compensation, source
of that rate, and estimated margin. `gigs_create_from_offer` applies the same
decision and snapshots it atomically with the gig.

For project-first work, use `job_posts_create`, `proposals_submit`, and
`proposals_accept`. A contract milestone is turned into executable work with
`contracts_dispatch_milestone`; submission and approval then update the
milestone, and the contract completes after all milestones are resolved.

## Three layers

1. **Instruction library** — atomic, versioned units. The dashboard creation
   flow exposes text, structured content, image, audio, and video instructions.
2. **Templates** — ordered compositions of pinned instruction versions
   with title + defaults + per-use overrides.
3. **Gigs** — immutable snapshots, composed at dispatch from a template +
   vars (or from instructions directly, or fully inline).

The dashboard supports the same core workflow: create instructions, build
template compositions, dispatch gigs from templates or selected instructions,
assign workers, and review submitted results.

Instructions are read-only by default. Add an explicit `body.response` contract
when a worker should return a note, files, or both for that step. Notes and files
are independently enabled and required, so a note cannot satisfy a required
recording upload:

```json
{
  "markdown": "Upload the final recordings.",
  "response": {
    "note": {"enabled": true, "required": false},
    "files": {
      "enabled": true,
      "required": true,
      "accept": ["video/*"],
      "min_items": 1,
      "max_items": 20,
      "max_size_mb": 2048
    }
  }
}
```

Template composition overrides may replace `body.response` for one template;
the resolved contract is frozen onto every dispatched gig. Legacy
`body.response_mode=optional|required` snapshots remain readable.

Templates may also define generic `response_rules` by instruction kind. A rule
is applied after instruction and composition overrides, so it enforces the same
worker response contract on every matching instruction in the dispatched gig:

```json
{
  "response_rules": [{
    "instruction_kind": "audio",
    "response": {
      "note": {"enabled": true, "required": false},
      "files": {
        "enabled": true,
        "required": true,
        "accept": ["video/*"],
        "min_items": 1,
        "max_items": 1,
        "max_size_mb": 2048
      }
    }
  }]
}
```

This is deliberately generic: templates can require any supported note/file
response for any non-input instruction kind. `gigs_create_from_template`
applies rules to fixed composition rows. For a varying composition, pass
`template_id` or `template_slug` to `gigs_create_from_instructions`; the gig
then inherits the active template's response rules, variable and schedule
defaults, and commercial rate context while keeping the supplied instruction
order. Rules are versioned with the template and frozen into the gig snapshot.

### Mixed text and images

Use a `content` instruction when one numbered worker card should mix text and
multiple images. Its body is an ordered block list; supported block types are
`markdown`, `image`, `callout`, and `divider`:

```json
{
  "blocks": [
    {"type": "markdown", "markdown": "## Starting position\nStand facing the camera."},
    {"type": "image", "storage_file_id": 123, "caption": "Correct position", "alt": "Full-body reference pose"},
    {"type": "callout", "tone": "tip", "text": "Keep your full body visible."},
    {"type": "divider"},
    {"type": "markdown", "markdown": "Continue with the recording brief."}
  ]
}
```

Images receive signed Storage URLs only when the worker loads the gig. Text,
captions, alternative text, and callouts support template variables. Omitting
`body.response` makes the whole content instruction read-only; the worker sees
no per-step note or upload control. Existing text and standalone media kinds
remain unchanged.

The worker page saves an assignment draft as notes and completed uploads change.
Each upload is bound to the instruction key and validated against its file type,
size, and count rules. Workers can remove or replace draft files and submit the
whole gig once every required instruction is complete. Resubmission remains open
until manager review; previous submitted revisions stay available for audit.

`result_schema`, `media_manifest`, `checklist`, and `variables` are
**derived** from the composition — never hand-authored.

## Scheduling, due dates, and access

Gigs keeps three independent times: `scheduled_for` is the intended recording
or work date, `due_at` is a soft submission target, and `access_expires_at` is
an optional hard worker-link expiry. Passing `due_at` marks the gig overdue and
emits `gig.overdue`, but workers can still read instructions, stream media,
upload, replace files, and submit.

Use `gigs_update_schedule` to edit all three controls. `gigs_extend_deadline`
remains as a compatibility alias for changing the soft due date. Access set as
7 or 14 days after due moves with it; a custom access date is preserved. If the
title or `vars` mention the original date, follow the schedule update with
`gigs_update` so the worker-facing record does not contradict itself:

```
gigs_update_schedule  id=11  scheduled_for=2026-08-20T18:00:00+03:00  due_at=2026-08-20T22:00:00+03:00  access_grace_days=14
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
awaiting review, work in flight with due-date urgency pills, and optionally
recent outcomes. Half size shows counts plus the three most urgent rows; full
size shows each enabled section. It refreshes live on the app's `gig.*` events
and reads the same `/gigs` summaries the panel uses — no widget-specific
backend. Operators can choose Scheduled for, Due by, or Both as the primary
date; Scheduled for is the default. Sections and the recent-outcome limit are
per-instance settings.

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
| `expired` | **Terminal.** Legacy or explicitly expired record; soft due dates no longer write this status |

**There is no `completed` status.** Work that was accepted is `reviewed`.
`gigs_list_open` accepts `completed`, `complete`, and `done` as aliases for
`reviewed` so the natural phrasing works, and rejects any other unknown value
with an error rather than returning an empty list.

`completed_at` marks a terminal transition such as `reviewed`, `cancelled`, or
a legacy `expired` record. Overdue gigs remain in their normal workflow status
and expose `overdue=true` plus `overdue_at`.

`rejected` is not a gig status: rejecting a submission records it on the
assignment and as a `gig_event`, and returns the gig to an earlier status
(`submitted`, `accepted`, `offered`, or `open`).

## App boundaries and dependencies

- `crm` (required) — workers are CRM contacts; notifications and timeline
  logging go through `crm.contacts_send_message` / `contacts_log_activity`.
- `storage` (required) — image/audio/video instruction media and worker submissions live
  under `/.gigs/` (configurable).
- `catalog` (required) — owns customer-facing products and immutable sell-side
  prices. Publishing an offer synchronizes its service product and package
  prices there.
- `bills` (optional) — owns worker accounts payable. `gigs_create_payable`
  creates an idempotent bill after work is reviewed; the
  `auto_create_worker_payables` config can do this automatically.
- `orders` (optional) — may create an order-sourced contract for service
  fulfillment. Gigs stores the order reference but remains the authority for
  scope, assignment, delivery, and worker compensation.
- `domains` (optional) — publishes branded worker-link hostnames through
  Domains-managed DNS and server-native ingress. No CDN app is required.

Gigs has no direct dependency on Checkout or Billing. Checkout is a payment
entry point and Billing is the customer receivables ledger; both operate
through Catalog/Orders rather than becoming part of worker compensation.

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

See `migrations/001_init.sql` and `migrations/004_marketplace.sql` for the
schema. Each Go file is one
surface: `workers.go`, `instructions.go`, `templates.go`, `gigs.go`,
`worker_page.go`, `composition.go` (derivation), `crm.go` /
`storage.go` (inter-app helpers), and `marketplace_*.go` (commercial model,
contracts, HTTP/MCP APIs, and Catalog/Bills integrations).

## Resumable worker uploads (0.5.2)

Upgrade the bound Storage app to **0.11.3 or newer before Gigs**. Worker links
send four binary 5 MiB parts concurrently through the platform's authenticated
Storage binding. Credentials stay on the server. Transient requests retry with
backoff; Pause preserves acknowledged parts. After a reload, select the same
original file to resume. Cancel explicitly abandons a partial upload. Storage
may expire idle partial uploads; those restart automatically on reselection.

Progress remains below 100% until Storage commits the file and Gigs saves its
receipt and draft association. Finalization continues when the browser closes,
and durable receipts recover interrupted completion responses. Per-file errors
include a stage and upload reference for diagnosis. Removed attachments remain
in Storage to protect shared/deduplicated objects and immutable submissions.

Drafts allow partial minimum-file counts; final submission enforces the full
contract. Every worker file reference is checked against assignment ownership
before accepting it or issuing a signed URL. Worker compensation JSON contains
only the worker's terms. Reviews, job awards and milestone dispatch claim their
state transactionally; payables use the worker whose assignment was reviewed.

Validation:

```sh
GOWORK=off go test -race ./...
bun test worker_upload.test.ts
# Build Storage in ../storage, then exercise real uploads (including SHA-256
# verification and a deliberately lost completion response):
GOWORK=off GIGS_TEST_STORAGE_BIN=/path/to/storage go test -run TestBinaryUploadWithRealStorage -v .
GOWORK=off GIGS_TEST_STORAGE_BIN=/path/to/storage GIGS_TEST_UPLOAD_BYTES=2000000000 go test -run TestBinaryUploadWithRealStorage -v .
```

The integration fixture uses temporary databases and disk storage. Its local
throughput is not an estimate of mobile-network or production S3 throughput.

## Financials (0.6.0)

Every gig has a generic financial section. Fixed fees and decimal rate × unit
agreements preserve their original currency; omitted amounts remain unknown.
Saving an amendment appends a revision, including the reason, confirmation
reference, timestamp and actor. Financial records prevent permanent gig deletion.

Accepting work does **not** approve compensation or record payment. Explicit
compensation approval records the accepted quantity, work amount, expenses,
canonical site/cost-center allocations and delivered Storage file references.
Shared allocations must sum to the approved total. Additional obligations and
credits explicitly reference an earlier obligation; they do not overwrite it.
Supplier, payroll and other settlement arrangements are supported.

Bills is optional. Connect it under the app's integrations to automatically
create an unapproved supplier payable after compensation approval. No Bills
installation is required for agreements, approvals, attribution or history.
Disabling `auto_create_approved_payables` prevents new automatic payables;
existing links still refresh. Bills creation never approves or pays the bill.
`gigs_financials_sync` retries failures and refreshes payment status; a durable
worker reconciles up to 25 due obligations each minute (five-minute per-record
interval). Bills events invalidate cached balances. Changed bindings cannot
redirect historical bill IDs to another installation.

Source identity includes Gigs installation, project, gig and obligation UUID.
The prepared request is persisted before calling Bills. Bills atomically claims
that identity and rejects conflicting replays. Historical bill links require
explicit adoption and matching vendor, amount and currency. Their payment
history remains marked as potentially incomplete. No historical approvals,
zero costs, confirmation dates or payments are invented by migration.

MCP: `gigs_financials_get`, `gigs_agreement_save`,
`gigs_compensation_approve`, `gigs_financials_sync`, `gigs_financial_facts`.
HTTP: GET `/financials/{gig_id}`; POST `/financials/{gig_id}/agreement`,
`/approve`, `/sync`; GET `/financials/{gig_id}/options`.
Use `expected_revision` for agreement changes and a stable `request_key` for
approval retries. Money is integer original-currency minor units; quantities
are decimal strings, multiplied with exact rational arithmetic and rounded
half up. Unknown approval amounts are rejected; known zero is explicit.

`gigs_financial_facts` exports paginated facts for Analytics. Each obligation
is one cost; its linked Bills entry is the same expense. Credits are negative
obligation facts. Recorded payments are a separate measure, included only on
the original obligation, not repeated on credit records. Sum by currency and
use explicit conversion policies if needed. Bill adjustments recorded directly
in Bills are exposed separately from approved Gigs amounts for reconciliation.
Do not sum both approval events and bill events as expenses, or both credit
facts and the bill's cumulative credit balance as additional reductions.

Sites and delivered files are optional: Content supplies canonical site IDs;
Storage supplies file identity, also used by Media. A Media installation is
not required and file bytes are never copied into financial records.
