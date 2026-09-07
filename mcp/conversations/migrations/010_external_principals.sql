-- External subjects have an app-local identity namespace. Negative IDs cannot
-- collide with platform users; existing positive owners and read marks stay intact.
CREATE TABLE external_principals (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 issuer_app TEXT NOT NULL,
 issuer_install_id TEXT NOT NULL,
 subject_type TEXT NOT NULL,
 subject_id TEXT NOT NULL,
 organization_id TEXT NOT NULL,
 UNIQUE(project_id,issuer_app,issuer_install_id,subject_type,subject_id,organization_id)
);
