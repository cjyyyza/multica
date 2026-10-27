ALTER TABLE agent_task_queue
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'execute',
    ADD COLUMN IF NOT EXISTS requirement_id UUID;
ALTER TABLE agent_task_queue DROP CONSTRAINT IF EXISTS agent_task_queue_mode_check;
ALTER TABLE agent_task_queue ADD CONSTRAINT agent_task_queue_mode_check
    CHECK (mode IN ('execute', 'plan')) NOT VALID;
