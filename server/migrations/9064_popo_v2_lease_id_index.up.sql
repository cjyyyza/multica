CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_v2_lease_id
    ON popo_v2_lease (lease_id);
