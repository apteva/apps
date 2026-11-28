# Telephony 0.33.0

## A hang-up never kills a call mid-pickup

- While an inbound call is being picked up (`answering`), only the session that
  claimed it can end it, identified by the session token the softphone now sends
  with `POST /calls/:id/hangup`. Any other client, including another tab or
  device of the same adviser or a supervisor, gets `{"ignored": true, "reason":
  "answering_elsewhere" | "no_session_token"}` and the call goes on. Every
  ignored hang-up is logged at INFO (`telephony hangup did not end call`) and
  written to `telephony_hangup_audit`.
- A phone user's hang-up while the call rings declines only that user's browser
  offers (`offer_declined`), so the ring group moves on. With no offer of theirs
  it is ignored (`no_active_offer`). Operator and agent hang-ups are unchanged.
- A claim whose browser never opens its media socket goes back to routing
  instead of ending as no-answer: 10 s for automatic pick-up, 25 s for a manual
  one. A claim that is attached, or whose carrier answer is in flight, waits.
  In a ring group, the claimed offer fails and the next one rings.
- Softphone: `hangup()` during a pick-up whose audio is not live releases the
  claim (falling back to a token-carrying hang-up if the call connected
  meanwhile). New `cancel(callId?)` is the safe "stop this call on this browser"
  action for host UIs: it declines this browser's offers, releases a pick-up in
  progress, and hangs up only this controller's own connected call.
  `client.hangup()` returns the outcome (`HangupResult`). `answer_failed`
  telemetry carries `session_generation`.
- Termination events record `termination.path` (`hangup`, `agent_tool`,
  `completion`, `transfer`, `ring_leg`) and `termination.session_generation`.

## Core gets a normal Close when an AI call ends

- Every AI call used to end with Core seeing the audio socket drop (the proxy's
  1006, recorded by Core as 1011 "realtime bridge transport error"), even when
  the caller simply hung up. The Core recovery link answered the bridge's Close
  itself and closed the Core connection without passing it on.
- The link now sends Core a Close frame first: the bridge's own code and reason,
  1000 "call ended" when the call ended without one, or 1011 with the cause when
  the link itself failed. It waits for Core's reply up to the forced close
  timeout (500 ms). This covers every carrier path.
- Direct SIP: 1000 "call ended" on a hang-up, 1000 "call transferred to an
  adviser" on a handover, and the failure's own code for RTP or Core errors.
  Carrier streams keep their existing reasons, such as 1000 "telnyx call media
  ended normally".
- Check after upgrading: the server's `realtime.proxy.closed` shows
  `close_code: 1000` without `unexpected EOF`, and Core's
  `realtime.bridge_disconnected` shows `normal: true` with the new reason.

## Telnyx: one application list instead of one read per number

- The connected-numbers view read every Telnyx Call Control application one by
  one, which hit Telnyx's rate limit (429, code 10011) on accounts with many
  numbers. It now reads `list_call_control_applications` (100 per page, at most
  10 pages, stopping once every needed application is found). An application
  missing from a complete list is reported as not found, with no extra request.
- A temporary list error (429, 5xx, timeout) is reported for every application
  without fanning out into single reads; a number verified in the last 24 h
  stays ready. After a 429 the view waits for Telnyx's `Retry-After` (5 s
  without the header, 60 s at most) before reading again. Placing calls is not
  affected.
- Only applications that matter are read: outbound readiness for numbers that
  do not dial over direct SIP, and the webhook check for Call Control routes.
- Clearer messages: a 404 says the application no longer exists and to apply
  carrier routing again; a 429 says the check was rate-limited and not yet
  verified.

## Tests and CI

- The coaching browser test waits for the microphone request before releasing
  the press (the first press loads the capture module first). It had failed on
  main since 0.29.0.
- CI gives the full Go suite a 20-minute timeout (it ran on Go's 10-minute
  default and had reached it) and the race groups 30 minutes.
