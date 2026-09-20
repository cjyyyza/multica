-- Outbound commands for a Windows bridge to long-poll. P1 type is send only.
-- unknown means POPO may already have the message; those rows must not return
-- to pending when a lease expires.
-- No foreign keys (repo rule).
CREATE TABLE popo_bridge_command (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL,
    bridge_id          UUID NOT NULL,
    installation_id    UUID NOT NULL,
    type               TEXT NOT NULL,
    delivery_id        UUID NOT NULL,
    payload            JSONB NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending',
    lease_expires_at   TIMESTAMPTZ,
    remote_message_id  TEXT,
    last_error         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
