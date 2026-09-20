-- Bridge-scoped grant to GET an attachment the send command named.
-- No foreign keys (repo rule).
CREATE TABLE popo_outbound_media_grant (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    bridge_id        UUID NOT NULL,
    installation_id  UUID NOT NULL,
    command_id       UUID NOT NULL,
    attachment_id    UUID NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL
);
