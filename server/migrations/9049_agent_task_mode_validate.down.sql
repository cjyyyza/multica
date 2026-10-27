ALTER TABLE agent_task_queue DROP CONSTRAINT IF EXISTS agent_task_queue_mode_check;
ALTER TABLE agent_task_queue ADD CONSTRAINT agent_task_queue_mode_check
    CHECK (mode IN ('execute', 'plan')) NOT VALID;
