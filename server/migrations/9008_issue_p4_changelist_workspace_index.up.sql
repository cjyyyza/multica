CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_p4_changelist_workspace
    ON issue_p4_changelist (workspace_id);
