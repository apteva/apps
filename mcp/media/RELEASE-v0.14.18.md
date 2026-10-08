# Media 0.14.18

Codex observations/automatic descriptions and `media_ask` now explicitly request
`reasoning.effort: low`, using the existing raw Responses integration tool. This
works with the installed platform adapter, whose chat compatibility path drops
reasoning controls, without requiring a platform upgrade.

The configured model, system/project instructions, question, transcript and
existing visual evidence are preserved. `media_ask` reports `reasoning_effort`.
Only completed assistant output text is accepted; incomplete, empty, refused,
error-bearing or unconfirmed-effort responses cannot be treated as observations
or crop approval. Reasoning items are never exposed as answers.

All retries use the same low-effort request within the existing overall budget.
Persisted description cooldowns retain their existing keys across the tool change.
Non-Codex providers keep their existing model, temperature and token parameters.
Codex continues to omit the token-limit parameter omitted by the previous adapter.
No rendering, Smart Crop, Storage or audio behavior changes.

Validation: 40 observation/description/ask race tests passed, including low-effort
visual requests, retry evidence reuse, preservation of existing cooldowns,
completed-output filtering and provider compatibility. Go vet and Darwin arm64,
Linux amd64 and Linux arm64 builds passed. A live request through the production
Codex connection completed with model `gpt-6.1-sol` and confirmed
`reasoning.effort: low`.
