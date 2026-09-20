CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_pairing_code_hash
    ON popo_bridge_pairing (code_hash);
