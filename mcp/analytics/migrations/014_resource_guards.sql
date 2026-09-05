CREATE TRIGGER limit_dashboard_widgets BEFORE INSERT ON dashboard_widgets
WHEN (SELECT COUNT(*) FROM dashboard_widgets WHERE dashboard_id=NEW.dashboard_id)>=50
BEGIN SELECT RAISE(ABORT,'dashboard limit is 50 widgets'); END;
CREATE TABLE collected_key_events(key_id INTEGER NOT NULL,event_id INTEGER NOT NULL,PRIMARY KEY(key_id,event_id));
