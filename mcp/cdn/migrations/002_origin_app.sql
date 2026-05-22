-- v0.2: name-addressed origins.
--
-- A zone can target an app by NAME (origin_app, e.g. "storage")
-- instead of a static origin_url:port. cdn passes "app://<name>" to
-- routes; apteva-server resolves the app's LIVE sidecar port at
-- request time, so the zone survives sidecar restarts/redeploys
-- (which reassign the local port). Exactly one of origin_url /
-- origin_app is set per zone — enforced in the app layer. origin_url
-- stays NOT NULL, so we store '' when origin_app is used.
ALTER TABLE zones ADD COLUMN origin_app TEXT NOT NULL DEFAULT '';
