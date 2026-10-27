CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_wakeup_requirement
    ON issue_wakeup (workspace_id, requirement_id, id)
    WHERE requirement_id IS NOT NULL;
