CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_claim
    ON requirement_outbox (next_attempt_at, created_at, id) WHERE status = 'pending';
