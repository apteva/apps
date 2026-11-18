-- How Instances reaches a registered host, and whether it is reachable now.
-- connect_mode: direct | vpn | reverse_tunnel. Existing VPN enrollments keep
-- working unchanged; they are labelled from their recorded VPN peer.
ALTER TABLE instances ADD COLUMN connect_mode TEXT NOT NULL DEFAULT 'direct';
ALTER TABLE instances ADD COLUMN connected INTEGER NOT NULL DEFAULT 0;
ALTER TABLE instances ADD COLUMN last_seen_at TEXT NOT NULL DEFAULT '';
UPDATE instances SET connect_mode = 'vpn' WHERE enrollment_peer != '';

-- One reverse tunnel per machine behind a router. The machine keeps
-- `ssh -N -R 127.0.0.1:<port>:127.0.0.1:<local_ssh_port>` open to this server;
-- Instances then reaches it at 127.0.0.1:<port>. The tunnel key can only open
-- that one listener (permitlisten) and is stored encrypted in the SSH vault.
CREATE TABLE reverse_tunnels (
    instance_id INTEGER PRIMARY KEY,
    port INTEGER NOT NULL UNIQUE,
    local_ssh_port INTEGER NOT NULL DEFAULT 22,
    public_key TEXT NOT NULL,
    credential_ref TEXT NOT NULL,
    server_host TEXT NOT NULL,
    server_port INTEGER NOT NULL,
    server_known_hosts TEXT NOT NULL,
    created_at TEXT NOT NULL
);
