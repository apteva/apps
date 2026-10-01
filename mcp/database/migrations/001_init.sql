-- SDK control database. Managed databases live beneath ctx.DataDir()/database.
CREATE TABLE database_app_version (version INTEGER NOT NULL);
INSERT INTO database_app_version VALUES (1);
