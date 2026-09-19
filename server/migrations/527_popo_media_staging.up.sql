-- Staging for Windows-fetched POPO files. The API never downloads from POPO.
-- Bytes are uploaded by the bridge, then promoted after member + message
-- ownership are confirmed. No foreign keys (repo rule).
CREATE TABLE popo_media_staging (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    bridge_id        UUID NOT NULL,
    installation_id  UUID,
    robot_id         TEXT NOT NULL DEFAULT '',
    event_id         TEXT NOT NULL,
    media_index      INT NOT NULL CHECK (media_index >= 0),
    filename         TEXT NOT NULL,
    mime_type        TEXT NOT NULL,
    size_bytes       BIGINT NOT NULL CHECK (size_bytes >= 0),
    kind             TEXT NOT NULL CHECK (kind IN ('image', 'file', 'audio', 'video')),
    status           TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'uploaded', 'failed')),
    storage_key      TEXT,
    storage_url      TEXT,
    error            TEXT,
    uploaded_at      TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
