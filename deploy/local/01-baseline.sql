-- Local Compose bootstrap snapshot of gitops migrations 0001-0003.
-- PostgreSQL applies this only when its data directory is first created.

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    owner       TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL DEFAULT '',
    category    TEXT NOT NULL DEFAULT '',
    pinned      BOOLEAN NOT NULL DEFAULT FALSE,
    status      TEXT NOT NULL DEFAULT 'building',
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    publishes   JSONB NOT NULL DEFAULT '[]'::jsonb,
    contest     JSONB,
    kind        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_owner_idx ON sessions (owner, updated_at DESC);
CREATE INDEX IF NOT EXISTS sessions_building_lease_idx
    ON sessions (lease_until) WHERE status = 'building';

CREATE TABLE IF NOT EXISTS events (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    seq        INT NOT NULL,
    type       TEXT NOT NULL,
    agent      TEXT,
    payload    JSONB,
    at         TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS messages (
    id         BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    agent      TEXT,
    content    TEXT NOT NULL,
    at         TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_session_idx ON messages (session_id, id);

CREATE TABLE IF NOT EXISTS jobs (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT NOT NULL,
    session_id  TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    owner       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    attempts    INT NOT NULL DEFAULT 0,
    error       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS jobs_status_idx ON jobs (status, id);
