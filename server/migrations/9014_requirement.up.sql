CREATE TABLE requirement (
    id                   UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id         UUID NOT NULL,
    source_host          TEXT NOT NULL,
    external_id          TEXT NOT NULL,
    source_url           TEXT NOT NULL DEFAULT '',
    source_snapshot      JSONB NOT NULL,
    source_digest        TEXT NOT NULL,
    source_lock_version  BIGINT,
    requester_id         UUID NOT NULL,
    agent_id             UUID NOT NULL,
    project_id           UUID,
    chat_session_id      UUID,
    issue_id             UUID,
    intake_operation_id  UUID,
    intake_state         TEXT NOT NULL DEFAULT 'intake_pending'
        CHECK (intake_state IN ('intake_pending', 'clarifying', 'awaiting_approval')),
    revision             BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    cancelled_at         TIMESTAMPTZ,
    cancel_reason        TEXT,
    last_error           TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
