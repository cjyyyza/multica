CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_lease_attempt
    ON popo_v2_lease (delivery_id, attempt);
