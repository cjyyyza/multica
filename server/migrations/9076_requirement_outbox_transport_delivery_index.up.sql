CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_transport_delivery
    ON requirement_outbox (transport_delivery_id) WHERE transport_delivery_id IS NOT NULL;
