CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_action_id
    ON requirement_action (id);
