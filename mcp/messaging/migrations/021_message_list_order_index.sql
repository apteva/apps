-- Unfiltered message_list orders a whole project by recency. Without this
-- index SQLite sorted every row of the project (bodies included) per page.
CREATE INDEX ix_msg_proj_time ON messages(project_id, created_at DESC, id DESC);
