CREATE TABLE IF NOT EXISTS ad_campaign_settings (
 project_id TEXT NOT NULL,
 ad_account_id INTEGER NOT NULL,
 campaign_id TEXT NOT NULL,
 settings_json TEXT NOT NULL,
 PRIMARY KEY(project_id,ad_account_id,campaign_id)
);
