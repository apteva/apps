# Telephony 0.17.0

Agents can press keypad keys on a call, for example to get through a phone
menu.

- New tool `telephony_send_dtmf`: `call_id`, `digits` (1-16 of `0-9`, `*`,
  `#`) and an optional `idempotency_key`. Only the agent that owns the call can
  use it, in its project, once the call is connected. Realtime call threads see
  it because they inherit the agent's tools.
- A retry with the same `idempotency_key` returns the first outcome
  (`duplicate: true`) without pressing the keys again. Reusing a key for other
  digits is refused; a press that failed may be retried with its key.
- Real DTMF tones, never speech: RFC 4733 events on direct SIP, the carrier's
  send-DTMF command on Telnyx, Plivo and Vonage.
- Twilio (new): Media Streams cannot carry DTMF, so the call's TwiML is
  replaced with `<Play digits>` followed by a new stream. The recording keeps
  running. The stop of the old stream is a planned handover: it is not
  recorded as a media failure or counted as a recovery, and media recovery
  waits until the new stream carries audio. A press while tones are still
  playing is refused with "retry shortly". If Twilio refuses the update,
  nothing changes. If the new stream never connects, the call is handled like
  any other Twilio stream loss.
- The browser softphone keypad uses the same path, so it now also works on
  Twilio calls.
- The AI is silent while tones play: about one second plus 0.6 s per key.
