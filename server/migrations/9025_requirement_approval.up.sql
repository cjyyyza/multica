CREATE TABLE requirement_approval (
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    requirement_id   UUID NOT NULL,
    candidate_id     UUID NOT NULL,
    candidate_digest TEXT NOT NULL,
    approved_by      UUID NOT NULL,
    approved_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    issue_id         UUID NOT NULL,
    initial_task_id  UUID NOT NULL
);
