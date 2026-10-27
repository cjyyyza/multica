CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_retry
    ON requirement_outbox (next_attempt_at, created_at, id) WHERE status IN ('pending', 'failed');
