-- Helix Swarm reviews mirrored for the issue sidebar. No foreign keys (repo rule).
CREATE TABLE swarm_review (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   UUID NOT NULL,
    swarm_url      TEXT NOT NULL,
    review_number  INTEGER NOT NULL CHECK (review_number > 0),
    changelist     TEXT NOT NULL DEFAULT '',
    title          TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    author         TEXT NOT NULL DEFAULT '',
    state          TEXT NOT NULL DEFAULT 'needsReview',
    html_url       TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
