-- Frozen originating chat for an issue created (or explicitly associated)
-- from a channel. Follow-up comments, run results, failures, cancels, and
-- important status changes go back to this route. No foreign keys (repo rule).
CREATE TABLE channel_issue_source (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    issue_id         UUID NOT NULL,
    installation_id  UUID NOT NULL,
    channel_type     TEXT NOT NULL,
    channel_chat_id  TEXT NOT NULL,
    chat_type        TEXT NOT NULL,
    binding_id       UUID,
    route_revision   BIGINT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
