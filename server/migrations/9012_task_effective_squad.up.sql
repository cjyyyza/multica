-- Share scheduling ownership across claim gates, capacity counts and wakeups.
-- Keep squad_id's existing leader-briefing meaning unchanged on worker rows.
CREATE FUNCTION task_effective_squad_id(p_task_id UUID)
RETURNS UUID
LANGUAGE sql STABLE
AS $$
    WITH RECURSIVE lineage AS (
        SELECT t.id, t.squad_id, t.issue_id, t.delegated_from_task_id,
               t.autopilot_run_id, t.context, 0 AS depth, ARRAY[t.id] AS visited
        FROM agent_task_queue t
        WHERE t.id = p_task_id
        UNION ALL
        SELECT p.id, p.squad_id, p.issue_id, p.delegated_from_task_id,
               p.autopilot_run_id, p.context, child.depth + 1, child.visited || p.id
        FROM lineage child
        JOIN agent_task_queue p ON p.id = child.delegated_from_task_id
        WHERE child.squad_id IS NULL
          AND NOT p.id = ANY(child.visited)
    ), candidates AS (
        SELECT COALESCE(
                   l.squad_id,
                   ar.squad_id,
                   CASE WHEN l.context->>'type' = 'quick_create'
                             AND l.context->>'squad_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                        THEN (l.context->>'squad_id')::uuid END
               ) AS squad_id,
               0 AS source_order, l.depth
        FROM lineage l
        LEFT JOIN autopilot_run ar ON ar.id = l.autopilot_run_id
        UNION ALL
        SELECT i.assignee_id, 1, l.depth
        FROM lineage l
        JOIN issue i ON i.id = l.issue_id AND i.assignee_type = 'squad'
    )
    SELECT squad_id
    FROM candidates
    WHERE squad_id IS NOT NULL
    ORDER BY source_order, depth
    LIMIT 1;
$$;
