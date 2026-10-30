CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_requirement_delegation_pending_guard
    ON agent_task_queue (issue_id, agent_id, COALESCE(comment_thread_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status IN ('queued', 'dispatched')
       OR (status = 'deferred' AND (context->>'channel_issue_media_pending' = 'true' OR context->>'requirement_delegation' = 'true'));
