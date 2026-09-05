ALTER TABLE contact_activities ADD COLUMN messaging_install_id INTEGER NOT NULL DEFAULT 0;
DROP INDEX ux_act_messaging_id;
CREATE UNIQUE INDEX ux_act_messaging_source ON contact_activities(project_id,messaging_install_id,messaging_id) WHERE messaging_id IS NOT NULL;
