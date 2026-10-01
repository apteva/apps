# Messaging v0.13.54

Includes the HTML-only email content fix from v0.13.53 and allows ten minutes
for initialization when upgrading a legacy Messaging database.

Production databases with a large email history can exceed the default
60-second startup budget while building the message-search index introduced
in v0.13.48. The longer explicit budget lets the existing transactional
migration finish before SDK and server health checks declare initialization
failed. The previous instance continues serving during activation.

No migration SQL, message data, routing configuration, or credentials change
in this release. Manifest compatibility and source builds are verified against
app-sdk v0.90.0.
