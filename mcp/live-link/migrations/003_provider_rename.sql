-- Live Link v0.4 — runs.provider becomes the strategy name.
--
-- v0.3 wrote 'cloudflared' (the binary) into runs.provider and used
-- runs.mode ('quick' | 'named') to disambiguate. v0.4 collapses these
-- onto runs.provider directly, with values matching the new Provider
-- interface ("cloudflare-quick" | "cloudflare-named" | future ones).
--
-- runs.mode is left in place for v0.3 panel-readers; new code reads
-- runs.provider. Future migration drops mode once nothing reads it.
UPDATE runs SET provider = 'cloudflare-named' WHERE provider = 'cloudflared' AND mode = 'named';
UPDATE runs SET provider = 'cloudflare-quick' WHERE provider = 'cloudflared' AND (mode = 'quick' OR mode = '' OR mode IS NULL);
