CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_delivery
    ON requirement_outbox (delivery_id);
