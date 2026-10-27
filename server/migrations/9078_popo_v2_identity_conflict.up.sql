CREATE TABLE popo_v2_identity_conflict (
    workspace_id  UUID NOT NULL,
    bridge_id     UUID NOT NULL,
    delivery_id   UUID NOT NULL,
    attempt       INTEGER NOT NULL CHECK (attempt > 0),
    command_id    UUID NOT NULL,
    lease_id      UUID NOT NULL,
    payload_hash  TEXT NOT NULL,
    trace_id      UUID NOT NULL,
    error         TEXT NOT NULL DEFAULT '',
    error_code    TEXT NOT NULL CHECK (error_code = 'IDENTITY_CONFLICT'),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$')
);
