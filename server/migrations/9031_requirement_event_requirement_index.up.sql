CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_event_requirement
    ON requirement_event (workspace_id, requirement_id, created_at, id);
