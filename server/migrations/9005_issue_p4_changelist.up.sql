-- Issue ↔ changelist links. A changelist can carry several issues (its
-- description can reference more than one) and an issue can carry several
-- changelists. link_source records who made the link: the reporting task, or a
-- person pasting a number. No foreign keys (repo rule).
CREATE TABLE issue_p4_changelist (
    issue_id       UUID NOT NULL,
    changelist_id  UUID NOT NULL,
    workspace_id   UUID NOT NULL,
    link_source    TEXT NOT NULL DEFAULT 'task' CHECK (link_source IN ('task', 'manual')),
    linked_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
