# CRM v0.9.9

Includes the missing inbound email content fixes from v0.9.8 and makes explicit
body recovery compatible with legacy numeric Messaging bindings.

The original-message lookup now calls the canonical Messaging app name, matching
CRM's existing send, status, and suppression wrappers. Some deployed bindings
provide an install ID without an app slug; using that absent slug produced an
invalid app-call URL during recovery previews.

The recovery tool continues to default to a dry run and verifies the original
project, install, and RFC Message-ID before filling only missing content.
Activities, conversations, complete bodies, and workflow state are preserved.
Regression coverage now verifies the exact app and message ID used for lookup.
