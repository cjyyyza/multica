CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_receipt_component
    ON popo_v2_receipt_component (delivery_id, attempt, component_id);
