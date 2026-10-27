CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_requirement
    ON agent_task_queue (requirement_id, id) WHERE requirement_id IS NOT NULL;
