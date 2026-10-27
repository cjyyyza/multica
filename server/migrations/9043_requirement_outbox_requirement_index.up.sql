CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_requirement
    ON requirement_outbox (workspace_id, requirement_id, created_at, id);
