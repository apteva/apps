# Signatures

Electronic signatures for PDFs stored in Apteva Storage, with a digitally
sealed completed PDF.

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
- Since v0.3: seals the completed PDF with a digital certification signature
  (see below) and can verify any Storage PDF's seal (`signatures_seal_verify`).

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

## Digital seal (v0.3)

After the last recipient finishes, the stamped document and its completion
page are sealed with one invisible **certification signature**
(`adbe.pkcs7.detached`, SHA-256, DocMDP "no changes allowed") that covers every
byte of the file. Acrobat shows it in the signature panel as "Certified by …";
editing or re-saving the PDF in any tool invalidates it. Like DocuSign's
envelope seal, the signature belongs to this install, not to each signer — the
signers' marks and the audit trail are what the seal protects.

Which certificate signs:

- **`seal_certificate` configured** — a base64 PKCS#12 (`.p12`/`.pfx`, password
  in `seal_certificate_password`) or PEM bundle with private key and chain. With
  a document-signing certificate from a CA on the Adobe Approved Trust List
  (AATL), Acrobat shows the seal as valid and trusted with no setup.
- **Nothing configured** — a self-signed RSA-3072 certificate is generated once
  per install and stored in the app DB. The seal is just as tamper-evident, but
  Acrobat reports the signer identity as unknown until the certificate
  (**Document seal → Download certificate** in the panel, or
  `/seal/certificate.cer`) is added under Preferences → Signatures →
  Identities & Trusted Certificates.

`timestamp_url` (RFC 3161, e.g. `http://timestamp.digicert.com`) embeds a
trusted signing time so the seal stays verifiable after the certificate
expires.

`signatures_seal_verify(file_id)` reports whether a PDF is sealed, whether the
signature is intact *and* reaches the end of the file (an appended incremental
update counts as tampering), and whether this install's seal made it.

## Assurance boundary

The signers' marks are a simple electronic-signature workflow; the seal adds
tamper evidence for the completed file. The completion page and audit JSON are
evidence, not a qualified certificate. This release does not claim AES, QES,
PAdES baseline profiles (it uses the `adbe.pkcs7.detached` sub-filter),
notarization, identity verification, or legal validity for every transaction
or jurisdiction.

Parallel routing, reusable templates, automatic scheduled reminders, provider
adapters, and long-term-validation (DSS/LTV) data are later additions.
