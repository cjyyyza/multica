-- Pending POPO replies for the Windows-local `multica popo gateway`.
-- The API process never opens POPO or dj01bot; it only stores text the
-- local CLI later POSTs to dj01bot /outbound (channel=popo_open).
-- No foreign keys (repo rule).
CREATE TABLE popo_outbound_queue (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    installation_id  UUID NOT NULL,
    chat_id          TEXT NOT NULL,
    robot_id         TEXT NOT NULL DEFAULT '',
    content          TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at     TIMESTAMPTZ,
    last_error       TEXT
);
