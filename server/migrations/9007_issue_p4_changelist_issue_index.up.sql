CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_p4_changelist_issue
    ON issue_p4_changelist (issue_id);
