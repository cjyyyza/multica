CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_inbound_composite_identity
    ON popo_inbound_event (bridge_id, robot_id, event_id)
    WHERE dedupe_enabled;
