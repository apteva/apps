# Telephony 0.16.1

AI replies on direct SIP start as fast as the model sends them again.

0.16.0 kept its drift lead (up to 180 ms) across the agent's listening
silence, so in live calls each new reply waited 148–195 ms behind queued
silence before the first word played.

- After 300 ms of continuous explicit silence the queue drains back to the
  initial reserve (60 ms) by dropping queued silence. The next reply's first
  word waits only for that reserve; the drift lead is rebuilt in the reply's own
  pauses.
- Replay of the ten captured reply streams: reply start delay mean 43 ms, max
  60 ms (drift off: 37 / 60 ms; 0.16.0: up to 160 ms); still no holes inside
  speech, no clicks or loud restarts. Lost provider time still lands in pauses
  rather than in words.
- Carrier media streams and `drift: "off"` behave as before.
