-- Workspace-scoped Windows bridge. The bearer token is returned once at
-- register time; this table stores only the SHA-256 hex. Revoking sets
-- status=revoked immediately; 45s without heartbeat is offline, not deleted.
-- No foreign keys (repo rule).
CREATE TABLE popo_bridge (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL,
    token_hash         TEXT NOT NULL,
    hostname           TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'active',
    last_heartbeat_at  TIMESTAMPTZ,
    robots_json        JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at         TIMESTAMPTZ,
    revoked_by         UUID
);
