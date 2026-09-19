-- QR registration sessions. Windows holds robot secrets; this table stores
-- only public robot identity and the Multica binding. No foreign keys (repo rule).
CREATE TABLE popo_registration (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    agent_id         UUID NOT NULL,
    initiator_id     UUID NOT NULL,
    bridge_id        UUID NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'awaiting_scan', 'success', 'error', 'expired')),
    qr_url           TEXT NOT NULL DEFAULT '',
    robot_id         TEXT NOT NULL DEFAULT '',
    robot_name       TEXT NOT NULL DEFAULT '',
    installation_id  UUID,
    error_reason     TEXT NOT NULL DEFAULT '',
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
