CREATE TABLE requirement_action (
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    requirement_id   UUID,
    actor_type       TEXT NOT NULL CHECK (actor_type IN ('member', 'agent', 'system')),
    actor_id         UUID NOT NULL,
    action           TEXT NOT NULL,
    idempotency_key  TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    request_hash     TEXT NOT NULL,
    response_status  INTEGER,
    response_body    JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
