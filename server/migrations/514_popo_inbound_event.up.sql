-- Proof that the Windows bridge submitted an inbound event. Distinct from
-- channel_inbound_message_dedup, which proves the engine claimed the message.
-- No foreign keys (repo rule).
CREATE TABLE popo_inbound_event (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    bridge_id        UUID NOT NULL,
    installation_id  UUID NOT NULL,
    event_id         TEXT NOT NULL,
    robot_id         TEXT NOT NULL,
    accepted         BOOLEAN NOT NULL,
    duplicate        BOOLEAN NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
