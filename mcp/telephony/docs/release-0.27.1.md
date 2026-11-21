# Telephony 0.27.1

- A simulated direct-SIP caller can be silent. The platform's Telnyx
  simulation sends `caller_audio: {"name": "silence"}` with no audio bytes
  when `incoming_call` asks for silence. 0.27.0 refused that hand-off as
  missing `wav_base64`. Now the caller sends silence, and a silent
  `sip_play` queues nothing.
- Only the Environment-only simulated carrier changes.
