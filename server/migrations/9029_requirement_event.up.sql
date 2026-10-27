CREATE TABLE requirement_event (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    requirement_id  UUID NOT NULL,
    actor_type      TEXT NOT NULL CHECK (actor_type IN ('member', 'agent', 'system')),
    actor_id        UUID,
    action          TEXT NOT NULL,
    from_state      TEXT,
    to_state        TEXT,
    entity_refs     JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
