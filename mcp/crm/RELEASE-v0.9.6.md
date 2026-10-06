# CRM v0.9.6

Fixes email replies splitting into new CRM conversations after sends through
AWS SES.

- Stores the RFC `message_id_header` returned by Messaging as the outbound
  activity and conversation root, keeping the provider ID separate.
- Matches older SES sends by the provider ID only for an SES `In-Reply-To` or
  `References` header in the same project and contact.
- Corrects a matched conversation root to the delivered RFC header so later
  replies use the right thread identifier.
- Keeps exact full-header matching for other providers and rejects unrelated
  domains or contacts in the SES fallback.
- Adds regression tests for full-header, legacy SES, alternate SES domain,
  cross-contact, and unrelated-domain replies.

No schema migration or bulk data rewrite is required. Existing split
conversations are not merged automatically; reconciliation needs a separate
review of their contact and activity history.
