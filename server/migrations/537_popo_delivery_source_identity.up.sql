-- Preserve already queued/delivered notifications when recovery becomes active.
-- Command ids stay unchanged, so Windows receipts remain valid.
WITH identified AS (
    SELECT id, installation_id, status, created_at,
      CASE
        WHEN payload->>'outbound_kind' IN ('task_reply', 'task_failed', 'task_cancelled')
          AND COALESCE(payload->>'task_id', '') <> ''
          THEN (payload->>'outbound_kind') || ':' || (payload->>'task_id')
        WHEN payload->>'outbound_kind' = 'issue_comment'
          AND COALESCE(payload->>'comment_id', '') <> ''
          THEN 'issue_comment:' || (payload->>'comment_id')
        WHEN payload->>'outbound_kind' = 'issue_created'
          AND COALESCE(payload->>'issue_id', '') <> ''
          THEN 'issue_created:' || (payload->>'issue_id')
        ELSE ''
      END AS source_key
    FROM popo_bridge_command
    WHERE type = 'send' AND installation_id IS NOT NULL
), ranked AS (
    SELECT *, row_number() OVER (
      PARTITION BY installation_id, source_key
      ORDER BY CASE status WHEN 'delivered' THEN 0 WHEN 'unknown' THEN 1 ELSE 2 END, created_at, id
    ) AS ordinal FROM identified WHERE source_key <> ''
)
UPDATE popo_bridge_command cmd
SET delivery_id = md5(r.installation_id::text || ':' || r.source_key)::uuid,
    payload = jsonb_set(cmd.payload, '{source_key}', to_jsonb(r.source_key))
FROM ranked r WHERE r.id = cmd.id AND r.ordinal = 1
  AND NOT EXISTS (
    SELECT 1 FROM popo_bridge_command other
    WHERE other.delivery_id = md5(r.installation_id::text || ':' || r.source_key)::uuid
      AND other.id <> cmd.id
  );
