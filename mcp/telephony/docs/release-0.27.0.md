# Telephony 0.27.0

Direct SIP can now be tested end to end inside an Environment. A simulated
Telnyx number that points at Telephony's direct-SIP (FQDN) connection makes a
real SIP call into Telephony, with real RTP audio. Outside an Environment
nothing changes.

## How it works

- The platform's Telnyx simulation records the FQDN connection Telephony
  registers. On `incoming_call` for such a number, it hands the call to
  Telephony (app-sdk v0.120.0 integration-simulation hand-off). The platform
  itself never speaks SIP.
- Telephony then plays the carrier. It places a SIP call over loopback into
  its own SIP gateway, from a separate socket:
  - it offers the connection's codecs (G722, G711U, G711A) and
    `telephone-event`;
  - it sends the hand-off's fixture audio as paced 20 ms RTP, then silence;
  - it measures the audio Telephony sends back;
  - it handles BYE, CANCEL and re-INVITE.
- The caller side reports into the simulation's `sip_calls` record:
  - `state`: `inviting`, `ringing`, `answered`, `ended` or `failed`;
  - `sip_call_id_header`, `responses`, and the timestamps `invited_at`,
    `ringing_at`, `answered_at` and `ended_at`;
  - `ended_by` (`caller` or `app`), `cause`, `q850`, `reason`, `sip_status`;
  - `codec`, `dtmf_sent`, `audio_queued`, `audio_played`, `error`;
  - `rtp`: `sent_packets`, `sent_ms`, `received_packets`, `received_ms`,
    `non_silent_ms`, `peak`, `rms`.
  It reports at every state change, and every 2 s while the call is up and
  something changed.
- Hand-off kinds:
  - `sip_inbound_call`: place the call.
  - `sip_hangup`: the caller hangs up. While ringing this sends CANCEL;
    once answered, BYE with a Q.850 `Reason`.
  - `sip_dtmf`: RFC 4733 digits; the call must be answered.
  - `sip_play`: queue more caller audio.

## Limits

- A call is placed only to this install's own gateway, on a loopback
  address and its listening port. Plain RTP only: an SRTP connection is
  refused, so set `sip_srtp=disabled`.
- Signaling is `udp` or `tcp`, and must match the gateway's.
- At most 8 simulated calls at once. A call rings for at most 3 minutes and
  lasts at most 10 minutes. Each audio clip is at most 120 s, and at most
  10 minutes of audio can be queued.
- Fixture audio arrives inline as base64 RIFF/WAVE PCM16 (`wav_base64`), so
  Telephony never fetches it through the Environment's proxy.

## Environment SIP settings

Inside an Environment (`APTEVA_ENVIRONMENT_ID` set), a loopback
`sip_public_ip` is accepted. Telephony then keeps the gateway on loopback:
- `sip_listen` and `sip_rtp_bind_ip` must be loopback; an unspecified RTP bind
  address becomes 127.0.0.1.
- `sip_listen` port `0` picks a free port once, at gateway start. That port is
  both the listening port and the one registered with the carrier.
- RTP range `0`–`0` uses OS-chosen ports.

Suggested `app_config` for Telephony in an Environment:

```yaml
sip_enabled: "true"
sip_transport: udp
sip_allow_insecure_signaling: "true"
sip_listen: 127.0.0.1:0
sip_public_host: 127.0.0.1
sip_public_ip: 127.0.0.1
sip_allowed_cidrs: telnyx=127.0.0.1/32
sip_srtp: disabled
sip_rtp_port_min: "0"
sip_rtp_port_max: "0"
```

Outside an Environment, a loopback `sip_public_ip` is still refused.

## Requirements

- app-sdk v0.120.0. Hand-offs need a platform build with the integration
  simulation `handoff` step and the report endpoint.
