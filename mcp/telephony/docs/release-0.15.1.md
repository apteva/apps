# Telephony 0.15.1

Removes most padding at AI-voice underruns on direct SIP.

0.15.0's drift compensation stretches frames by pitch periods, so a reply's
audio is no longer always whole 20 ms packets. When the queue ran dry, the
remaining partial packet was padded with zeros (faded), about 80 ms per minute
in the live test.

- With drift compensation on, an underrun's partial packet is completed with a
  continuation of the voice's last pitch period and fades out at the packet's
  end: packet-loss concealment before the loss. It runs only on periodic or
  quiet audio, never adds a sharper step than the audio already has, and falls
  back to the faded zero padding otherwise (for example loud unvoiced sounds).
- A sub-fade remainder that can be continued no longer forces the preceding
  full packet to fade.
- New diagnostics field `filled_ms` beside `padding_ms`.
- Offline replay of the captured OpenAI pacing (six 18 s replies): padding
  115 ms → 26 ms; total silence inside speech 435 ms → 346 ms (2224 ms with
  drift off); no click edges, loud restarts or pitch changes; healthy delivery
  unchanged.
- Carrier media streams and `drift: "off"` behave as before.
