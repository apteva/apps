# Telephony 0.21.0

- Faster WebRTC pick-up. While a call rings, an idle softphone that is offered
  it (WebRTC or automatic transport) opens its WebRTC connection, with no
  microphone or playback, carrying nothing. Answering then only claims the
  call: the claim moves the call onto that connection, the browser attaches
  its microphone and says so, and only then is the carrier answered, as
  before. The time after answering no longer includes setting up the WebRTC
  connection (0.3–0.6 s in a local Chromium test, directly and through a TURN
  relay); the answer used that same peer connection, with no reconnect. If the
  connection is not ready or was lost, the answer connects as it always did.
  - `POST /softphone/standby/{call_id}` (permission `call.answer`) authorizes
    one connection for a call this browser is offered now. At most 4 per user,
    8 per call and 64 per install. It ends with the offer, after 60 s, when
    another browser claims the call, or when the browser closes it.
  - The answer accepts `standby_id` and reports `standby: "promoted"` or
    `"unavailable"`. A promoted connection waits up to 60 s for the
    microphone; if it never comes, the claim is given back and the call rings
    again.
  - The install setting `softphone_webrtc_standby` (on by default) turns it
    off. The SDK option `ringStandby: false` turns it off for one softphone.
- Pick-ups no longer wait for each other. A manual pick-up held an
  install-wide lock for its whole request, so one pick-up in an install was
  handled at a time. Only automatic pick-ups take that lock now, around their
  policy check.
- The claim is one database transaction: the claim, the other ring offers,
  the attachment, the owner and capacity, and the media grant, instead of
  four or five commits on Telephony's single database connection. A failure
  leaves the call as it was.
- One `softphone answer timing` log line per answered call: `claim_ms`,
  `session_returned_ms`, `browser_attach_ms`, `rtc_ready_ms`,
  `carrier_answer_ms`, `media_start_ms` and `total_ms`, in milliseconds after
  the answer request arrived (server clock; a stage done during the ring
  counts as 0), plus `transport` and `standby`.
- Housekeeping could not delete an outbound direct SIP call older than
  retention (foreign key error 787 from `outbound_sip_calls`), and the error
  rolled back the whole batch, which then failed again every run. Its SIP
  details are now deleted with the call (migration 076). A call that still
  cannot be deleted is skipped for a day, and the rest of the batch is
  deleted.
