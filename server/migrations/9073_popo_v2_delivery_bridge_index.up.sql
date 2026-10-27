CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_delivery_bridge
    ON popo_v2_delivery (bridge_id, status, expires_at);
