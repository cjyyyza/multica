CREATE TABLE requirement_participant (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    requirement_id  UUID NOT NULL,
    user_id         UUID NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('requester', 'approver', 'viewer')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
