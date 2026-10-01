# Messaging v0.13.53

Supplies readable text for HTML-only inbound email while preserving its original
HTML body.

- MIME parsing derives `body_text` when the provider supplies no meaningful
  text/plain content. This covers both SES and Gmail inbound ingestion.
- Dispatch also normalizes old HTML-only records on retry, persisting the text
  and passing both `body_text` and `body_html` to downstream apps.
- Provider-supplied text and original HTML are preserved. Script, stylesheet,
  and document metadata are excluded from generated text.
- Source builds and tests use app-sdk v0.90.0.

No schema migration, automatic redispatch, or bulk rewrite is introduced.
Regression tests cover HTML-only MIME parsing, safe extraction, whitespace-only
text, provider-text preservation, and the complete downstream dispatch payload.
