CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_swarm_review_workspace
    ON issue_swarm_review (workspace_id);
