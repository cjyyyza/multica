CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_p4_changelist_issue_changelist
    ON issue_p4_changelist (issue_id, changelist_id);
