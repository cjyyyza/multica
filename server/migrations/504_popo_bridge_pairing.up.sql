-- One-time Windows-bridge pairing codes. The plaintext code is returned
-- once at mint time; this table stores only the SHA-256 hex.
-- No foreign keys (repo rule).
CREATE TABLE popo_bridge_pairing (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL,
    code_hash     TEXT NOT NULL,
    created_by    UUID NOT NULL,
    hostname      TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMPTZ NOT NULL,
    consumed_at   TIMESTAMPTZ,
    bridge_id     UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
