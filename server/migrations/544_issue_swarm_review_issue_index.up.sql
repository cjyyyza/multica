CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_swarm_review_issue
    ON issue_swarm_review (issue_id);
