-- Per-render output folder so render submissions can pick where the
-- result lands instead of all renders going to the install's
-- configured render_output_folder. The config value remains the
-- fallback for callers that don't pass an explicit folder.
--
-- Default '' distinguishes "caller didn't specify, use the config"
-- from a real "/" root upload.
ALTER TABLE renders ADD COLUMN output_folder TEXT NOT NULL DEFAULT '';
