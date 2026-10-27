-- Conservatively project legacy v2 text deliveries onto the single command
-- ledger. Uncertain outcomes never become automatically sendable.
UPDATE popo_bridge_command AS cmd
SET id = delivery.command_id
FROM popo_v2_delivery AS delivery
WHERE cmd.delivery_id = delivery.delivery_id
  AND cmd.requirement_id IS NOT NULL
  AND cmd.id <> delivery.command_id
  AND NOT EXISTS (
      SELECT 1 FROM popo_bridge_command AS existing WHERE existing.id = delivery.command_id
  );

WITH mapped AS (
    SELECT
        cmd.id,
        delivery.bridge_id,
        delivery.installation_id,
        delivery.recipient_binding_id,
        delivery.payload_snapshot,
        delivery.payload_hash,
        delivery.latest_attempt,
        delivery.expires_at,
        delivery.remote_message_id,
        delivery.partial_delivery,
        delivery.retryable AS delivery_retryable,
        delivery.not_sent AS delivery_not_sent,
        delivery.evidence_finalized AS delivery_evidence_finalized,
        delivery.created_at,
        delivery.updated_at,
        outbox.accepted_at,
        (SELECT max(lease.created_at) FROM popo_v2_lease lease WHERE lease.delivery_id = delivery.delivery_id) AS last_lease_at,
        CASE
            WHEN delivery.status = 'delivered' THEN 'delivered'
            WHEN delivery.status IN ('failed', 'cancelled', 'expired', 'revoked')
                AND delivery.not_sent AND delivery.evidence_finalized THEN 'failed'
            WHEN delivery.status = 'pending'
                AND delivery.remote_message_id IS NULL
                AND NOT delivery.partial_delivery
                AND NOT EXISTS (
                    SELECT 1 FROM popo_v2_lease lease
                    WHERE lease.delivery_id = delivery.delivery_id AND lease.state <> 'pending'
                ) THEN 'pending'
            ELSE 'unknown'
        END AS mapped_status
    FROM popo_v2_delivery AS delivery
    JOIN popo_bridge_command AS cmd ON cmd.delivery_id = delivery.delivery_id
    JOIN requirement_outbox AS outbox
      ON outbox.transport_delivery_id = delivery.delivery_id
     AND outbox.requirement_id = cmd.requirement_id
    WHERE cmd.requirement_id IS NOT NULL
)
UPDATE popo_bridge_command AS cmd
SET bridge_id = COALESCE(mapped.bridge_id, cmd.bridge_id),
    installation_id = COALESCE(mapped.installation_id, cmd.installation_id),
    recipient_binding_id = mapped.recipient_binding_id,
    payload = mapped.payload_snapshot,
    payload_hash = mapped.payload_hash,
    attempt = GREATEST(cmd.attempt, mapped.latest_attempt),
    expires_at = mapped.expires_at,
    accepted_at = COALESCE(cmd.accepted_at, mapped.accepted_at, mapped.created_at),
    dispatched_at = CASE
        WHEN mapped.latest_attempt > 0 AND mapped.mapped_status <> 'pending'
            THEN COALESCE(cmd.dispatched_at, mapped.last_lease_at, mapped.updated_at)
        ELSE NULL
    END,
    remote_message_id = mapped.remote_message_id,
    partial_delivery = mapped.partial_delivery,
    status = mapped.mapped_status,
    not_sent = mapped.mapped_status = 'pending'
        OR (mapped.mapped_status = 'failed' AND mapped.delivery_not_sent),
    retryable = mapped.mapped_status = 'failed' AND mapped.delivery_retryable,
    evidence_finalized = mapped.mapped_status = 'delivered'
        OR (mapped.mapped_status = 'failed' AND mapped.delivery_evidence_finalized),
    needs_reconcile = mapped.mapped_status = 'unknown',
    delivered_at = CASE WHEN mapped.mapped_status = 'delivered' THEN COALESCE(cmd.delivered_at, mapped.updated_at) ELSE cmd.delivered_at END,
    unknown_at = CASE WHEN mapped.mapped_status = 'unknown' THEN COALESCE(cmd.unknown_at, now()) ELSE cmd.unknown_at END,
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
FROM mapped
WHERE cmd.id = mapped.id;

-- Any unresolved identity mismatch is manual-reconcile evidence, never a send.
UPDATE popo_bridge_command AS cmd
SET status = 'unknown',
    unknown_at = COALESCE(unknown_at, now()),
    not_sent = false,
    retryable = false,
    evidence_finalized = false,
    needs_reconcile = true,
    lease_id = NULL,
    lease_expires_at = NULL,
    last_error = 'POPO v2 command identity could not be adopted safely',
    updated_at = now()
FROM popo_v2_delivery AS delivery
WHERE cmd.delivery_id = delivery.delivery_id
  AND cmd.requirement_id IS NOT NULL
  AND cmd.id <> delivery.command_id;

-- Existing bridges that support v1 continue with the single command protocol.
UPDATE popo_bridge
SET selected_protocol_version = 1,
    protocol_health_json = protocol_health_json || '{"single_command_takeover":true}'::jsonb
WHERE selected_protocol_version = 2
  AND supported_protocol_versions @> '[1]'::jsonb;
