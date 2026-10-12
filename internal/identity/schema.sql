CREATE TABLE IF NOT EXISTS identity_accounts (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  verified BOOLEAN NOT NULL DEFAULT FALSE,
  disabled BOOLEAN NOT NULL DEFAULT FALSE,
  last_workspace_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS identity_workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('personal','team')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS identity_members (
  workspace_id TEXT NOT NULL REFERENCES identity_workspaces(id) ON DELETE CASCADE,
  account_id TEXT NOT NULL REFERENCES identity_accounts(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner','editor')),
  PRIMARY KEY (workspace_id, account_id)
);
CREATE TABLE IF NOT EXISTS identity_sessions (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  account_id TEXT NOT NULL REFERENCES identity_accounts(id) ON DELETE CASCADE,
  workspace_id TEXT NOT NULL REFERENCES identity_workspaces(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS identity_credentials (
  token_hash TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES identity_accounts(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL CHECK (purpose IN ('verify','reset')),
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS identity_invitations (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES identity_workspaces(id) ON DELETE CASCADE,
  email TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('owner','editor')),
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  accepted_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS identity_audit (
  id BIGSERIAL PRIMARY KEY,
  actor_id TEXT REFERENCES identity_accounts(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  subject_id TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS identity_auth_limits (
  key TEXT PRIMARY KEY,
  window_started_at TIMESTAMPTZ NOT NULL,
  attempts INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS identity_auth_limits_window_idx ON identity_auth_limits (window_started_at);
