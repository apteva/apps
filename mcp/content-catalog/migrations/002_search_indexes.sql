-- Search reads only Catalog relationships; these indexes keep destination and
-- review filters efficient as the number of sessions grows.
CREATE INDEX ix_assets_review_session ON assets(project_id, review_status, session_id, id);
CREATE INDEX ix_target_assets_asset ON release_target_assets(project_id, asset_id, target_id);
CREATE INDEX ix_targets_destination_account ON release_targets(project_id, destination, account_ref, current_status, id);
CREATE INDEX ix_releases_project_created ON releases(project_id, created_at DESC, id DESC);
