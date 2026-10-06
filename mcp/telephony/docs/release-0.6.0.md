# Telephony 0.6.0

One call-progress model for every carrier, normalized termination reasons,
answering machine detection, pushed call status for the softphone, and a
richer exported softphone. Everything is additive: no status value, event
topic, or socket message changed meaning, and answering machine detection is
off unless a project or a call turns it on.

## Call progress

- Consumers now see `initiated → ringing → answered → terminal` on every
  carrier. Telnyx never sends a ringing webhook, so Telephony synthesizes
  `call.ringing` for an outbound call once the carrier accepts the dial. The
  event payload carries `synthesized: true` and `source: telephony`.
- Telnyx `time_limit` hangups end as `completed` instead of `failed`.

## Termination

- Every terminal call carries `termination.reason` from a fixed set:
  `completed`, `busy`, `no_answer`, `rejected`, `invalid_number`,
  `unreachable`, `canceled`, `time_limit`, `failed`. The raw carrier cause,
  SIP code, and initiator stay alongside it. The five terminal statuses are
  unchanged.
- `termination`, `answered_by`, and `ended_at` appear in the calls list, the
  panel payload, and every lifecycle event.
- `GET /calls/{id}` reads one call with the same visibility rules as the list.
  Application users need the `call.read` action, as for the list.

## Answering machine detection

- `telephony_place_call` and the softphone `POST /softphone/place` accept
  `machine_detection` (`off`, `detect`, `premium`) and
  `machine_detection_action` (`notify`, `hangup`).
- `telephony_outbound_settings_get` and `telephony_outbound_settings_set`
  hold the project defaults next to the recording settings.
- Twilio and SignalWire use asynchronous `AnsweredBy` callbacks, Telnyx uses
  the `call.machine.detection.ended` and premium webhooks, Plivo uses its
  asynchronous `Machine` callback. Vonage does not support detection and
  rejects non-off modes.
- `answered_by` is persisted as `human`, `machine`, `fax`, `silence`, or
  `unknown`, included in every call payload, and announced once per call with
  the new `call.machine_detected` event. With the `hangup` action Telephony
  ends the call when a machine or fax answers and records the termination
  cause `machine_detected`.

## Softphone

- The media socket's text channel now pushes `call.status` frames with the
  status, answered and ended times, termination, and `answered_by`. Polling
  remains as the fallback for older clients.
- The exported softphone snapshot gains `phase` (`idle`, `placing`,
  `ringing`, `connected`, `ended`), `termination`, `answeredBy`, and
  `endedAt`, retained until the next dial or answer. `detail` behaves as
  before.
- A new `ringback` option plays a locally synthesized tone through the SDK's
  own audio context from ringing until answer, so it follows the chosen output
  device. It is off by default in the SDK and on in the bundled Telephony
  panel. Cadences are provided per country with France as the default.

## Compatibility

Schema changes add columns with defaults and one settings table. Existing
tests that pin Telnyx event sequences gained the synthesized ringing event.
