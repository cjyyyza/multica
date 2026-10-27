CREATE TABLE requirement_candidate (
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL,
    requirement_id      UUID NOT NULL,
    revision            BIGINT NOT NULL CHECK (revision > 0),
    snapshot            JSONB NOT NULL,
    digest              TEXT NOT NULL,
    created_by_task_id  UUID NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
