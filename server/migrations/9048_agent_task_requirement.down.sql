ALTER TABLE agent_task_queue
    DROP CONSTRAINT IF EXISTS agent_task_queue_mode_check,
    DROP COLUMN IF EXISTS requirement_id,
    DROP COLUMN IF EXISTS mode;
