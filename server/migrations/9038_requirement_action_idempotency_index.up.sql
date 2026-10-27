CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_action_idempotency
    ON requirement_action (workspace_id, actor_type, actor_id, action, idempotency_key);
