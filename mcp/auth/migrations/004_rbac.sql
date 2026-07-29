-- 004_rbac — organization-scoped roles and permissions.
--
-- Authorization data is deliberately separate from users.metadata_json:
-- metadata is self-service profile data, while every table below is only
-- mutated through authenticated admin HTTP routes or MCP tools.

ALTER TABLE users ADD COLUMN authorization_version INTEGER NOT NULL DEFAULT 1;

CREATE TABLE auth_roles (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  organization_id   INTEGER NOT NULL,
  key               TEXT NOT NULL,
  name              TEXT NOT NULL,
  description       TEXT,
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ux_auth_roles_key
  ON auth_roles(project_id, organization_id, key);
CREATE INDEX ix_auth_roles_org
  ON auth_roles(project_id, organization_id, created_at);

CREATE TABLE auth_permissions (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  organization_id   INTEGER NOT NULL,
  key               TEXT NOT NULL,
  name              TEXT NOT NULL,
  description       TEXT,
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ux_auth_permissions_key
  ON auth_permissions(project_id, organization_id, key);
CREATE INDEX ix_auth_permissions_org
  ON auth_permissions(project_id, organization_id, created_at);

CREATE TABLE auth_role_permissions (
  project_id        TEXT NOT NULL,
  organization_id   INTEGER NOT NULL,
  role_id           INTEGER NOT NULL,
  permission_id     INTEGER NOT NULL,
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (role_id, permission_id),
  FOREIGN KEY (role_id) REFERENCES auth_roles(id) ON DELETE CASCADE,
  FOREIGN KEY (permission_id) REFERENCES auth_permissions(id) ON DELETE CASCADE
);
CREATE INDEX ix_auth_role_permissions_org
  ON auth_role_permissions(project_id, organization_id, role_id);
CREATE INDEX ix_auth_role_permissions_permission
  ON auth_role_permissions(permission_id);

CREATE TABLE auth_user_roles (
  project_id        TEXT NOT NULL,
  organization_id   INTEGER NOT NULL,
  user_id           INTEGER NOT NULL,
  role_id           INTEGER NOT NULL,
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (user_id, role_id),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (role_id) REFERENCES auth_roles(id) ON DELETE CASCADE
);
CREATE INDEX ix_auth_user_roles_org
  ON auth_user_roles(project_id, organization_id, user_id);
CREATE INDEX ix_auth_user_roles_role
  ON auth_user_roles(role_id);
