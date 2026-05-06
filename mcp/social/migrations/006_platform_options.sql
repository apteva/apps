-- Per-target overrides. JSON blob storing the flat per-target options
-- agents pass via post_create's targets[] array — body override (used
-- when this specific target should publish different copy from the
-- post-level body) plus any platform-specific keys (YouTube: title,
-- visibility, category, …; Reddit: subreddit, flair_id, …). Sparse:
-- agents only populate keys for the platform of the target's account,
-- and most targets won't have any overrides at all.
ALTER TABLE post_targets ADD COLUMN options TEXT;
