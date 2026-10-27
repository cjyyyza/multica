CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_event_id
    ON requirement_event (id);
