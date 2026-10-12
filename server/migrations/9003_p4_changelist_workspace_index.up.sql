CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_p4_changelist_workspace
    ON p4_changelist (workspace_id);
