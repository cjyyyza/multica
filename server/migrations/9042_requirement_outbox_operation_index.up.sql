CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_operation
    ON requirement_outbox (operation_id) WHERE operation_id IS NOT NULL;
