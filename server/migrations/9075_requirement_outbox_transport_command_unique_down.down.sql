CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_outbox_transport_command
    ON requirement_outbox (transport_command_id) WHERE transport_command_id IS NOT NULL;
