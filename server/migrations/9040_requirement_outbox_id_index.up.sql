CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_id
    ON requirement_outbox (id);
