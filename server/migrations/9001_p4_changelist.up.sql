-- Perforce changelists as first-class objects, so an issue can show the CL a
-- task produced and the daemon can poll it to submission. No foreign keys
-- (repo rule); issue.sql / workspace_delete.sql sweep the rows.
--
-- server_key is the Helix serverCluster when the server reports one, else the
-- lowercased port. It exists because an edge server answers on a different
-- port than the commit server while numbering the same changelists, so the
-- port alone cannot identify a changelist.
--
-- original_number is the number the changelist had while pending; a submit
-- renumbers it and `number` follows. Identity stays on original_number.
CREATE TABLE p4_changelist (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    -- Empty until a daemon claims the row. A manually linked changelist has
    -- no daemon until the first one that serves its port reports on it.
    daemon_id        TEXT NOT NULL DEFAULT '',
    server_key       TEXT NOT NULL,
    port             TEXT NOT NULL,
    charset          TEXT NOT NULL DEFAULT '',
    number           INTEGER NOT NULL CHECK (number > 0),
    original_number  INTEGER NOT NULL CHECK (original_number > 0),
    client           TEXT NOT NULL DEFAULT '',
    p4_user          TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'shelved', 'submitted', 'deleted')),
    description      TEXT NOT NULL DEFAULT '',
    ticket           TEXT NOT NULL DEFAULT '',
    files_count      INTEGER NOT NULL DEFAULT 0,
    exclusive_files  JSONB NOT NULL DEFAULT '[]',
    warnings         JSONB NOT NULL DEFAULT '[]',
    swarm_review_id  UUID,
    task_id          UUID,
    submitted_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
