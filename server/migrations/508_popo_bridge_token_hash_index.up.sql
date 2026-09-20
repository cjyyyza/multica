CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_popo_bridge_token_hash
    ON popo_bridge (token_hash);
