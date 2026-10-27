CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_v2_recovery
    ON requirement_outbox (updated_at, id)
    WHERE transport_protocol = 2 AND status IN ('accepted', 'failed', 'unknown');
