# Signatures

Simple electronic signatures for PDFs stored in Apteva Storage.

## What v0.2 does

- Creates one-document envelopes from a Storage PDF — picked or uploaded
  straight from the dashboard panel.
- Supports ordered signers and approvers.
- Visual field placement: the panel renders the PDF (vendored pdf.js) and
  fields are dragged/resized on the page; coordinates stay normalized 0–1.
- The signing page renders the document with each recipient's field boxes
  overlaid at their real positions; typed values preview live in the box.
- Signature fields offer Type or Draw; drawn signatures submit as PNG data
  URLs and are stamped as images into the completed PDF.
- Places typed signature, initials, date, text, and checkbox values on the PDF.
- Activates secure, recipient-specific signing links (`?project_id=` is part
  of the link — anonymous no_auth routing resolves the install from it).
- Sends invitations and reminders when the optional Messaging app is bound.
- Saves the completed PDF and JSON audit summary back to Storage; drawn
  signatures are recorded in the audit JSON as their SHA-256, not raw bytes.
- Preserves source and completed SHA-256 hashes and an append-only audit trail.

Storage is the only required app. Messaging is optional. Manual mode returns a
link only through the explicit, permission-gated
`signatures_recipient_link_create` tool or the dashboard's **Copy link**
action.

## Lifecycle

```text
draft -> sent -> completed
             -> declined | voided | expired
```

Recipients sign sequentially. Only the current recipient can have an active
link. Generating a new link revokes the previous one.

## Local development

```bash
env GOCACHE=/private/tmp/codex-go-build go test ./...
env GOCACHE=/private/tmp/codex-go-build go build .
```

The module is listed in the workspace-root `go.work`, so local builds use the
checked-out `app-sdk`. Standalone installs resolve the pinned SDK version from
`go.mod`.

## Assurance boundary

This release records a simple electronic-signature workflow. Its completion
page and audit JSON are evidence, not a qualified certificate. It does not
claim AES, QES, PAdES, notarization, identity verification, or legal validity
for every transaction or jurisdiction.

Typed signature marks are implemented first. Drawn/uploaded signature images,
parallel routing, reusable templates, automatic scheduled reminders, provider
adapters, and independently signed evidence bundles are later additions.
