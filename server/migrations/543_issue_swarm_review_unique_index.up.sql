CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_swarm_review_issue_review
    ON issue_swarm_review (issue_id, swarm_review_id);
