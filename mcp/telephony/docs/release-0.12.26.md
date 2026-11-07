# Telephony 0.12.26

## Microphone-to-SIP progress after expired audio

The SIP pacer now discards expired frames from its bounded queue within the same
send tick, then transmits at most one eligible frame. Previously every discarded
frame consumed a 20 ms tick. With microphone arrivals continuing every 20 ms,
that could keep the queue permanently behind and produce zero outgoing RTP.

The 250 ms source-age limit, queue capacities, RTP cadence, codec settings,
recording, hold, DTMF and buffered AI playback policy remain in place. Source
metadata retains its original time across handoffs. There is no timestamp
refresh that would disguise old speech as new, and no new worker or polling.
At most the queue depth present at the tick is inspected, bounded by capacity.

Live pacer telemetry exposes `max_source_age_ms` and `max_residence_ms` separately.
Expired-frame events retain `frame_age_ms` and `queue_residence_ms`. The dashboard
metrics include `carrier_pacer_max_source_age_ms` and
`carrier_pacer_max_residence_ms`. Age maxima use atomics, without a new lock or
logging/I/O on healthy packet dequeue.

## Crossed SIP hangups

A local BYE receiving 481 is treated as successful termination only when the
same validated dialog has already ended through the peer. Applies to the shared
inbound and outbound engines, including Telnyx, Twilio and DIDWW. The real 481 is
retained as `sip_bye_crossed`, with peer-ended signaling state. Unconfirmed 481,
other error responses, transport failures and timeouts remain errors.

## Regression coverage and interpretation

- Natural speech through the actual capture-release queue, source envelope,
  resampler, SIP pacer, G.722/PCMA/PCMU codecs and local UDP. Healthy output is
  compared byte-for-byte with independently encoded reference frames.
- A delayed-source/short-backlog scenario reproducing the previous all-discard
  failure; eligible speech resumes without a catch-up RTP burst.
- Ten virtual minutes with an exact reference of the old dequeue rule. At
  180 ms upstream age, old behavior sends 0 ms; corrected behavior sends
  600,000 ms with one initial 20 ms expired frame discarded. Healthy 60 ms
  upstream timing sends 600,000 ms with zero drops under both implementations.
- Expired-only queues drain completely; fresh audio behind expired frames is
  available in the same tick; buffered AI audio remains eligible.
- Actual local SIP crossed-BYE exchange, plus confirmed/unconfirmed 481 and
  other response-error cases. New regressions join CI race checks.

Synthetic timestamps and local peers reproduce the software failure mechanism;
they do not reconstruct the exact cause of source age in individual production
calls or prove carrier-network performance. Publishing does not install,
restart or alter production/staging.

Both manifests/source refs and generated frontend metadata use 0.12.26. SDK
remains at v0.102.0, verified as the latest published tag by ancestry.
