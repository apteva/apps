CREATE TABLE IF NOT EXISTS instance_firewalls (
 instance_id INTEGER PRIMARY KEY,
 record TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instance_firewall_owners (
 instance_id INTEGER NOT NULL,
 owner TEXT NOT NULL,
 rules TEXT NOT NULL,
 PRIMARY KEY(instance_id, owner)
);
