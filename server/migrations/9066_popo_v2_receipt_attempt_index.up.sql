CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_receipt_attempt
    ON popo_v2_receipt (delivery_id, attempt);
