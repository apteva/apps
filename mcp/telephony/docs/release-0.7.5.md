# Telephony 0.7.5

Configurable connected-call duration with separate setup and media-recovery
watchdogs. Includes all Telephony 0.7.4 changes. This is a source release;
no staging or production installation is updated.

## Changes

- Default new calls to a four-hour connected duration, configurable from 60 to
  14400 seconds through `connected_call_max_duration_seconds`. Preserve outbound
  AI `max_duration_sec` overrides and snapshot settings per call.
- Separate connected duration from setup timeout and transport recovery. Start
  the connected clock from confirmed carrier answer or inbound connected media;
  outbound early media and local answer status alone do not start it.
- Persist deadlines through reconnects, transfers, hold, duplicate callbacks and
  restarts. External ring-group legs inherit the parent clock. Disconnected media
  gets a separate bounded recovery timer; silence and mute do not trigger it.
- Complete intentional duration expiry with `termination.reason: time_limit`
  and `cause: max_duration`, preserving answered/missed classification. Persist
  termination intent before the carrier command and retry failed hangups safely.
- Display “Durée maximale atteinte” in French and “Maximum call duration reached”
  in English. Export the label helper and durable duration fields for clients.
- Pass outbound limits to Telnyx, Twilio, Plivo and Sinch. Align DIDWW's local SIP
  safety timer and answer evidence. All adapters retain local enforcement.
- Pin app-sdk v0.89.1 and regenerate the frontend assets.

## Upgrade behavior

Migration 036 preserves inferable legacy call budgets, including one hour: the
old records cannot distinguish an explicit one-hour override from the old
default. Only new calls use the four-hour default. In-flight carrier limits
already submitted cannot be lifted by the database migration.

## Verification

Full Go short suite, focused duration/lifecycle/AI race tests, local compiled
sidecar and Chromium integration tests, frontend regression tests, typechecks,
Go vet, standalone build and frontend asset build. Duration regressions use an
explicit clock to cover multi-hour boundaries, concurrent expiry, failed hangup
retries, restart/reconnect behavior, migration and provider command parameters.

## Limits

No live calls were placed. Tests do not certify a real four-hour PSTN connection;
carrier account limits may be shorter. Consuming apps should use the stable
termination reason or exported label helper for their own translated displays.
No integration JSON, app registry or installation configuration was changed.
See `CALL-DURATION.md` for settings, migration details and verification scope.
