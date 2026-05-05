-- Backfill empty visibility values left behind by the pre-fix
-- chunked-upload path. handleUploadsCollection used to call
-- visibilityOrDefault(body.Visibility) which returns "" when the
-- client omits visibility — that empty string overrode the column's
-- default and ended up in the DB. Newer uploads use
-- effectiveVisibility(ctx, ...) which falls back to the install's
-- default, but old rows are stuck with "" and render as "undefined"
-- in the dashboard.
UPDATE files SET visibility = 'private' WHERE visibility = '' OR visibility IS NULL;
