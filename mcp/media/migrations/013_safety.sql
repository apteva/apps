-- 013_audience_rating.sql
--
-- Audience-level rating populated by the describer's multimodal LLM
-- call (same single call that produces the description — no separate
-- moderation pipeline). Three columns:
--
--   audience_rating — one of {unrated, general, mature, adult}.
--                     unrated  = describer hasn't run or parse failed
--                     general  = appropriate for all audiences
--                     mature   = 13+ themes (suggestive, mild language,
--                                alcohol/drugs, mild violence)
--                     adult    = 18+ (explicit sexual content, graphic
--                                violence, gore, hate symbols)
--
--   audience_reasoning — short LLM explanation. Empty for general and
--                        unrated. Captured verbatim; UI shows it on
--                        the badge hover + in the detail-drawer.
--
--   audience_updated_at — TIMESTAMP of the last write. Describer's
--                         skip-already-rated gate checks
--                         "audience_rating != 'unrated'" so resetting
--                         to 'unrated' (manual re-evaluate) triggers
--                         a fresh describer pass.
--
-- Reset for re-evaluation: media_set_audience_rating({rating:"unrated"})
-- clears the column + reasoning, and the next describer sweep
-- re-classifies.

ALTER TABLE media ADD COLUMN audience_rating TEXT NOT NULL DEFAULT 'unrated';
ALTER TABLE media ADD COLUMN audience_reasoning TEXT NOT NULL DEFAULT '';
ALTER TABLE media ADD COLUMN audience_updated_at TIMESTAMP;

-- Helpful for media_search's audience_rating filter (panel + agents
-- routinely filter "exclude adult").
CREATE INDEX IF NOT EXISTS ix_media_audience_rating
  ON media (project_id, audience_rating);
