CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_issue
    ON requirement (issue_id);
