CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_inbound_event_workspace
    ON popo_inbound_event (workspace_id, created_at);
