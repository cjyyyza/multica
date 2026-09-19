CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_inbound_event_install_event
    ON popo_inbound_event (installation_id, event_id);
