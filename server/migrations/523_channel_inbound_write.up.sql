-- Idempotency ledger for channel issue/comment/cancel writes. Distinct from
-- channel_inbound_message_dedup: a claimed chat message is not proof that the
-- corresponding issue or comment was created. Persist this row in the same
-- transaction as the business write. No foreign keys (repo rule).
CREATE TABLE channel_inbound_write (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    installation_id  UUID NOT NULL,
    channel_type     TEXT NOT NULL,
    message_id       TEXT NOT NULL,
    kind             TEXT NOT NULL,
    issue_id         UUID,
    comment_id       UUID,
    task_id          UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
