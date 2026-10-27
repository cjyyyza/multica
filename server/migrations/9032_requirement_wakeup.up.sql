CREATE TABLE requirement_wakeup (
    id                        UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id              UUID NOT NULL,
    requirement_id            UUID NOT NULL,
    question_comment_id       UUID NOT NULL,
    current_wakeup_id         UUID NOT NULL,
    generation                BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    state                     TEXT NOT NULL DEFAULT 'armed',
    issue_revision            BIGINT NOT NULL CHECK (issue_revision > 0),
    last_triggered_comment_id UUID,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);
