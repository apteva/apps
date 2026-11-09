# Telephony 0.18.1

- Manual pickup claims the inbound offer while the browser prepares audio. The
  carrier connects when media attaches. Microphone denial or cancellation
  releases the claim, including when the browser receives the response late.
- Pickup Diagnostics links the browser's audio setup times to the server's
  claim arrival and decision times through a shared request ID.
- A normal caller hangup on direct SIP records the media as disconnected when
  teardown interrupts a Core WebSocket read. A Core socket failure that happens
  first still records a media error.
