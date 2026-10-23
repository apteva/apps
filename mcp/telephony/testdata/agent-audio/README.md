# Agent interruption fixture

`caller-interrupt-fr.wav` is synthetic French speech generated with the OpenAI
API for this local test on 2026-10-23. PCM16 mono, 16 kHz. It contains no real
caller data or credentials.

Text: “Attendez, je voudrais simplement prendre rendez-vous mardi. Quel jour
ai-je demandé ?”

The same clip passed through the local Telnyx media handler, actual Server
proxy, actual Core bridge and real `gpt-live-1` session. The paid test is opt-in;
ordinary regression tests replay this saved clip without API access.
