CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_command_delivery_id
    ON popo_bridge_command (delivery_id);
