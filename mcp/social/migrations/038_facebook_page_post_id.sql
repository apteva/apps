-- Facebook photo publishes can return both the photo and its enclosing Page post.
ALTER TABLE post_targets ADD COLUMN facebook_page_post_id TEXT;
CREATE INDEX idx_post_targets_facebook_page_post ON post_targets(social_account_id, facebook_page_post_id)
WHERE facebook_page_post_id IS NOT NULL;
