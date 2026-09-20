CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_swarm_review_workspace_url_number
    ON swarm_review (workspace_id, swarm_url, review_number);
