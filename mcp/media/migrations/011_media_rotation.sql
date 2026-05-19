-- 011_media_rotation.sql
--
-- Display rotation in degrees (0/90/180/270). Lifted from ffprobe's
-- displaymatrix side_data at probe time. Renderers read this column
-- and prepend a transpose filter (+ pass -noautorotate to ffmpeg) so
-- the filter chain operates on the displayed-orientation frame
-- rather than the raw codec frame.
--
-- Width/Height columns now hold DISPLAY-space dims (post-rotation).
-- Pre-migration rows carry the old codec-space W/H + rotation=0
-- until the next reindex flips them; storage's file.added re-emits
-- on sidecar restart, OR the operator can run media_reindex to
-- backfill manually.

ALTER TABLE media ADD COLUMN rotation INTEGER NOT NULL DEFAULT 0;
