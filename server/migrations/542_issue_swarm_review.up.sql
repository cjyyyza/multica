-- Issue ↔ Swarm review links. No foreign keys (repo rule).
CREATE TABLE issue_swarm_review (
    issue_id         UUID NOT NULL,
    swarm_review_id  UUID NOT NULL,
    workspace_id     UUID NOT NULL,
    close_intent     BOOLEAN NOT NULL DEFAULT false,
    linked_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
