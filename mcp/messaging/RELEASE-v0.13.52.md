# Messaging v0.13.52

Separates email's RFC `Message-ID` from the provider's opaque message ID in
`send_message` results.

- Returns `message_id_header` alongside `provider_message_id`, and documents
  which value callers should use for email threading.
- Persists the Gmail header already generated for raw sends in the response.
- For SES sends, derives the delivered header from the provider ID and the
  bound connection's region, then persists it with the send result. If the
  region is unavailable, the header remains empty rather than returning a
  bare provider ID as an RFC header.
- Adds send contract tests for SES simple and raw sends and Gmail.

This is an additive response field. The existing provider ID and message
delivery behavior remain unchanged.
