# Simple Signatures App Proposal

Status: Proposed MVP

## Decision

Build a small first-party `signatures` app for sending a PDF to one or more
people for signature.

Storage is the only required app. Messaging is optional. The first version has
no dependency on Auth, Analytics, Jobs, Workflows, SaaS, CRM, or an external
signing provider.

```text
PDF in Storage
  -> create envelope
  -> add recipients and signature fields
  -> send or copy signing links
  -> recipients sign in order
  -> completed PDF saved to Storage
```

The MVP provides a simple electronic-signature workflow with a clear audit
trail. It does not claim AES, QES, PAdES, notarization, identity verification,
or guaranteed legal validity for every document or jurisdiction.

## Goal

An agent or project member should be able to:

1. Select a PDF already in Storage.
2. Add one or more recipients.
3. Place required fields on the PDF.
4. Send invitations through Messaging or copy secure links manually.
5. See who has viewed and signed.
6. Download the completed PDF and its audit summary.

External signers do not need an Apteva account.

## Dependencies

### Required

#### Storage

Storage owns all document bytes. Signatures stores only file IDs, hashes,
field definitions, recipient state, and audit records.

It is used for:

- The original PDF.
- The completed PDF.
- Optional signature images.
- The JSON audit summary.

The original file is never overwritten.

### Optional

#### Messaging

When installed, Messaging sends:

- Initial signing invitations.
- Manual reminders.
- Completion notices.

When Messaging is not installed, the sender can copy a short-lived signing
link from the dashboard or request one through a permission-gated MCP tool.
Signing itself continues to work normally.

The send operation must use an explicit delivery mode:

- `messaging`: send invitations through Messaging; fail clearly when the app
  is unavailable.
- `manual`: activate the envelope without sending anything and allow the
  sender to generate recipient links.

No other app dependency is needed for the MVP.

## Scope

### Included

- One PDF per envelope.
- One or more recipients.
- Sequential signing order.
- Signature, initials, date, text, and checkbox fields.
- Typed signature marks.
- Secure signer links.
- Optional invitation and reminder emails through Messaging.
- Draft, sent, completed, declined, voided, and expired states.
- Completed PDF stored in Storage.
- Basic audit trail and document hashes.
- Project dashboard and MCP tools.

### Deferred

- Auth accounts and MFA.
- Analytics integration.
- Automatic scheduled reminders.
- Background expiry jobs.
- SaaS plans and usage limits.
- CRM contact lookup.
- Workflow integration beyond emitted events.
- Parallel routing and conditional routing.
- Reusable envelope templates.
- Bulk sending.
- Drawn and uploaded signature marks.
- SMS OTP and identity verification.
- External providers such as DocuSeal or DocuSign.
- Cryptographic PDF certificates, AES, QES, and PAdES.
- Multiple documents in one envelope.
- Custom signing domains and advanced branding.

## App Boundaries

- Storage owns file bytes and download URLs.
- Signatures owns envelopes, recipients, fields, secure links, signatures,
  status, finalization, and the audit trail.
- Messaging only delivers notifications when available.
- Documents may generate an upstream PDF, but Signatures only needs its Storage
  file ID and therefore does not depend on Documents.

## Envelope Lifecycle

```text
draft
  -> sent
  -> completed

sent
  -> declined | voided | expired
```

An envelope may remain `sent` while some recipients have signed. Recipient
status provides the detailed progress.

Rules:

- Drafts can be edited.
- Sending freezes the source hash, recipients, fields, and signing order.
- Recipients sign sequentially.
- Only the current recipient receives an active signing link.
- The next recipient becomes active after the current recipient signs.
- Declining, voiding, expiring, and completing are terminal.
- Repeated sends and signature submissions are idempotent.
- Expiration is checked whenever an envelope or signer session is accessed;
  the MVP does not need a background scheduler.

## Recipient Model

Each recipient has:

- Name.
- Email, optional in manual delivery mode.
- Role: `signer` or `approver`.
- Signing order.
- Status: `pending`, `ready`, `viewed`, `signed`, `approved`, or `declined`.

The MVP supports one recipient at each signing order. Parallel recipients can
be added later without changing the envelope model.

Recipient details are copied into the envelope when it is sent. They are not
live references to another app.

## Fields

MVP field types:

- `signature`
- `initials`
- `date_signed`
- `text`
- `checkbox`

Each field stores:

- Recipient ID.
- PDF page number.
- Normalized `x`, `y`, `width`, and `height` values.
- Required flag.
- Optional label and text validation.

Before sending, the app verifies that the PDF exists, its hash can be read,
all coordinates are valid, and every signer has at least one signature field.

## Signer Experience

The signer opens a secure link and sees:

1. Sender name, document title, and optional message.
2. The PDF being signed.
3. Only the fields assigned to that signer.
4. Simple consent text and a required consent checkbox.
5. A final confirmation before submission.
6. A completion screen after signing.

The signer types a signature and separately confirms the full legal name used
for it. Drawn and uploaded signature marks can be added after the typed flow is
reliable.

Signer links use high-entropy random tokens. Only token hashes are stored in
the database. Tokens are recipient-specific, revocable, expiring, and omitted
from logs, events, and ordinary MCP responses.

The permission-gated manual-link tool may return a newly generated link to an
authorized sender. The link is never shown in chat cards, list responses, or
audit events.

## Finalization And Audit

When the last recipient finishes, Signatures:

1. Confirms that the current Storage file still matches the frozen source
   SHA-256.
2. Places the completed field values and signature marks onto a copy of the
   PDF.
3. Appends a simple completion page containing the envelope ID, recipient
   names, actions, timestamps, and document hash.
4. Uploads the completed PDF to Storage.
5. Uploads a JSON audit summary to Storage.
6. Marks the envelope completed and emits `envelope.completed`.

The audit trail records:

- Envelope creation and send.
- Link creation and notification delivery result.
- Recipient view.
- Consent.
- Signature, approval, or decline.
- Manual reminder.
- Void or expiry.
- Finalization result.

Audit events are append-only. They include server timestamps and safe request
metadata but never raw tokens or signature source bytes. Source and completed
file hashes make later document changes detectable. A separately signed,
independently verifiable evidence bundle can be added after the MVP.

## Persistence

Use the app's SQLite database with five project-scoped tables:

- `envelopes`
- `recipients`
- `fields`
- `field_values`
- `audit_events`

The `envelopes` row stores source and completed Storage file IDs, source and
completed SHA-256 hashes, title, message, status, expiry, delivery mode, and
timestamps.

Lifecycle mutations accept an idempotency key. Every lookup and mutation is
gated by `project_id`.

## Permissions

- `signatures.read`: list and view envelope status and audit records.
- `signatures.send`: create drafts, configure recipients and fields, send,
  generate manual links, and send reminders.
- `signatures.manage`: void envelopes and change project defaults.

Public signer routes use the recipient token, not agent permissions. A signer
can access only their frozen document, assigned fields, and current action.

Agents cannot provide consent or submit a signature for a recipient through
MCP.

## MCP Surface

- `signatures_envelopes_create`
- `signatures_envelopes_update`
- `signatures_envelopes_get`
- `signatures_envelopes_list`
- `signatures_recipients_set`
- `signatures_fields_set`
- `signatures_envelopes_validate`
- `signatures_envelopes_send`
- `signatures_recipient_link_create`
- `signatures_envelopes_remind`
- `signatures_envelopes_void`
- `signatures_envelopes_finalize`
- `signatures_audit_get`

`signatures_envelopes_create` accepts a Storage `source_file_id`, title,
optional message, expiry, and idempotency key.

`signatures_envelopes_send` accepts `delivery_mode: messaging|manual`. It does
not return signing links. Manual links are generated separately with
`signatures_recipient_link_create` so access is explicit and auditable.

## HTTP Surface

- `GET /sign/:token`: load the signer page and recipient session.
- `GET /sign/:token/document`: stream the frozen PDF.
- `POST /sign/:token/consent`: record the current consent text.
- `POST /sign/:token/complete`: validate and submit assigned fields.
- `POST /sign/:token/decline`: decline with an optional reason.

Public routes are rate-limited and use strict body limits, origin policy, and
security headers. Responses never reveal other recipients or their status.

## Events

Publish only the core lifecycle events:

- `envelope.sent`
- `envelope.completed`
- `envelope.declined`
- `envelope.voided`
- `envelope.expired`
- `recipient.viewed`
- `recipient.signed`
- `recipient.approved`

This keeps future Workflows integration possible without requiring Workflows
now. Event payloads contain IDs, status, safe recipient metadata, timestamps,
and completed file ID when available. They exclude tokens, signature images,
and document bytes.

## Dashboard UI

The initial dashboard has three screens:

### Envelopes

- List envelopes by status.
- Show recipients and current signer.
- Open source and completed files.
- Create a new envelope.

### Draft editor

- Choose a PDF from Storage.
- Add ordered recipients.
- Place fields on page previews.
- Choose Messaging or manual delivery.
- Set expiry.
- Validate and send.

### Envelope detail

- Show recipient progress and audit timeline.
- Copy the active recipient link in manual mode.
- Send a manual reminder when Messaging is available.
- Void an active envelope.
- Open the completed PDF and audit summary.

## Manifest Shape

```yaml
schema: apteva-app/v1

name: signatures
display_name: Signatures
version: 0.1.0
description: |
  Send PDFs for simple electronic signature. Supports ordered recipients,
  secure signing links, visible signature fields, completed PDFs, and an
  audit trail. Storage is required; Messaging is optional.

scopes: [project]

requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: storage
      version: ">=0.10.0"
      optional: false
    - name: messaging
      optional: true
      reason: Sends signing invitations, reminders, and completion notices.
```

## Build Order

1. Manifest, database migration, store, and envelope state transitions.
2. Storage client and source validation.
3. Recipient tokens and public signer routes.
4. PDF field placement and finalization.
5. MCP tools and permissions.
6. Dashboard draft editor and envelope detail.
7. Optional Messaging delivery.
8. Security, idempotency, and cross-project tests.

## MVP Acceptance Criteria

- Storage is the only app required to install Signatures.
- A sender can create and complete an envelope without Messaging installed.
- Messaging delivery fails clearly when selected but unavailable.
- A PDF can be signed by two people in sequence.
- External signers do not need accounts.
- A recipient cannot access another recipient's fields or action.
- The source PDF cannot change after sending without finalization failing.
- Completed PDF and JSON audit summary are written to Storage.
- Repeated send and complete requests do not duplicate actions.
- Tokens and signature source images never appear in logs or lifecycle events.
- Expired, declined, voided, and completed envelopes reject further signing.
- Cross-project access tests fail closed.

## Later Expansion

Once the simple version is reliable, it can expand with:

- Auth-backed signers and MFA.
- Analytics dashboards.
- Jobs-powered reminders, expiry, and retention.
- SaaS plans and usage limits.
- CRM recipient selection.
- Workflow recipes.
- Parallel and conditional routing.
- Templates and bulk sending.
- DocuSeal and DocuSign adapters.
- SMS OTP and identity verification.
- Independently signed evidence bundles and cryptographic PDF signatures.
- AES, QES, PAdES, and qualified timestamp providers.
