# Call duration and recovery policy

Telephony separates connected-call duration from ringing/routing/startup and
media-transport recovery. This policy applies to human and AI calls, inbound
and outbound, and external ring-group legs. No per-customer behavior is used.

## Configuration

| Installation setting | Default | Accepted range |
|---|---:|---:|
| `connected_call_max_duration_seconds` | 14400 (four hours) | 60–14400 seconds |
| `call_setup_timeout_seconds` | 3600 | 60–3600 seconds |
| `call_media_recovery_timeout_seconds` | 120 | 30–600 seconds |

Invalid setting values fall back to the documented default. The existing
four-hour supported ceiling is retained. An outbound AI tool call can override
its duration through `max_duration_sec`. Browser calls inherit the configured
policy. Each new call snapshots its settings; later config changes and duplicate
inbound webhooks do not change that call's budget.

The setup ceiling is separate from shorter adviser offers, ringing timeouts,
routing budgets and AI-startup/activation deadlines. Those safeguards still run.

## Clock and persistence

The connected clock starts at the first confirmed carrier answer or, for an
inbound call without an answer callback, the first connected-media observation.
An outbound media socket may exist before PSTN answer, so outbound media alone
is insufficient; DIDWW/SIP's successful answer/ACK records carrier evidence.
A local `answered` status alone never starts the clock. IVR time after carrier
answer counts toward the total connection duration.

`duration_started_at`, `connected_deadline_at` and `max_duration_sec` are durable
and exposed in call responses. Reconnect, transfer, hold and restart do not
extend the clock. External ring-group legs inherit an existing parent clock.

Migration 036 preserves inferable legacy per-call durations, including one hour.
The old database cannot distinguish an explicit one-hour override from its
default, so it would be unsafe to silently extend those budgets. The four-hour
default applies to new calls. Old carrier-side limits already sent for in-flight
calls cannot be lifted by this local database migration.

## Recovery and termination

Media transport errors and disconnections after media connected start a separate,
bounded recovery deadline. Repeated errors/restarts do not extend an existing
recovery deadline; reconnection clears it. Silence, mute and hold without a
transport disconnection do not start this timer. Existing socket health checks,
bounded writes and reconnection handling remain; this is not a speech-activity
watchdog or a promise to detect every possible upstream media fault.

Intentional maximum-duration expiry completes the call with
`termination.reason: "time_limit"`, cause `max_duration`, and initiator
`telephony`. The reason is persisted before the hangup request and survives late
carrier callbacks. Answered/missed-call classification is preserved. Ordinary
setup expiry remains no-answer; media-recovery expiry remains a transport fault.
Carrier termination failure stays retryable, rather than pretending the remote
call ended successfully.

The frontend exports `callTerminationLabel(termination, locale)`, returning
**“Durée maximale atteinte”** for French and **“Maximum call duration reached”**
for English. The native call panel uses this label. Consuming applications should
use the stable termination reason for their own translated presentation.

## Carriers

Telephony enforces the policy locally for every adapter. It also sends the
configured outbound limit through existing Telnyx `time_limit_secs`, Twilio
`TimeLimit`, Plivo `time_limit`, and Sinch dial-command duration fields. DIDWW's
local SIP duration safety timer uses the same budget and termination reason.
Other adapters retain local enforcement; account/provider limits may be shorter.
No integration JSON, carrier account, staging or production setting was changed.

## Verification

`TestCallDuration*` covers settings and overrides, inbound deduplication, positive
answer evidence, outbound early media, calls beyond one hour, exact expiry,
late callbacks, concurrent workers, canceled calls, media recovery, reconnect,
policy persistence, migration, inherited external-leg clocks, carrier command
parameters, and selection by the real lifecycle worker. Multi-hour boundaries
use an explicit clock without waiting for real hours or placing phone calls.
Frontend tests cover the translated duration label. Existing unit, browser,
human/AI routing and carrier integration regressions must also pass.

A real carrier's long-call behavior still requires a separately authorized
end-to-end call. These tests do not certify a live PSTN connection for four hours.
