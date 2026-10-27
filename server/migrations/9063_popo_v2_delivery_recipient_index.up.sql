CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_delivery_recipient
    ON popo_v2_delivery (command_id, recipient_binding_id);
