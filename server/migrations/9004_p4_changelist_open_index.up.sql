-- The daemon poll reads only unfinished changelists, which stay a small slice
-- of the table once work is submitted, so the index is partial on them.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_p4_changelist_open
    ON p4_changelist (workspace_id, status)
    WHERE status IN ('pending', 'shelved');
