-- Requirement POPO deliveries use popo_bridge_command as their single ledger.
-- The row starts before a recipient binding, installation or bridge is known
-- (bridge_id and installation_id stay NULL while the delivery is in backlog),
-- then carries lease/attempt fencing and receipt evidence after preparation.
ALTER TABLE popo_bridge_command ALTER COLUMN bridge_id DROP NOT NULL;

ALTER TABLE popo_bridge_command DROP CONSTRAINT IF EXISTS popo_bridge_command_attempt_check;
ALTER TABLE popo_bridge_command
    ADD CONSTRAINT popo_bridge_command_attempt_check CHECK (attempt >= 0);

ALTER TABLE popo_bridge_command
    ADD COLUMN requirement_id UUID,
    ADD COLUMN requirement_kind TEXT,
    ADD COLUMN occurrence_id UUID,
    ADD COLUMN operation_id UUID,
    ADD COLUMN recipient_user_id UUID,
    ADD COLUMN recipient_binding_id UUID,
    ADD COLUMN requirement_revision BIGINT CHECK (requirement_revision IS NULL OR requirement_revision > 0),
    ADD COLUMN issue_revision BIGINT CHECK (issue_revision IS NULL OR issue_revision > 0),
    ADD COLUMN snapshot_payload JSONB,
    ADD COLUMN request_hash TEXT,
    ADD COLUMN payload_hash TEXT,
    ADD COLUMN lease_id UUID,
    ADD COLUMN dispatched_at TIMESTAMPTZ,
    ADD COLUMN not_sent BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN retryable BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN partial_delivery BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN evidence_finalized BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN needs_reconcile BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN expires_at TIMESTAMPTZ,
    ADD COLUMN accepted_at TIMESTAMPTZ,
    ADD COLUMN delivered_at TIMESTAMPTZ,
    ADD COLUMN superseded_at TIMESTAMPTZ,
    ADD COLUMN unknown_at TIMESTAMPTZ;

-- Upgrading a database that still had requirement_outbox rows keeps them in the
-- single ledger. Existing command rows are updated in place so a delivery never
-- gains two ledger rows.
INSERT INTO popo_bridge_command (
    workspace_id, bridge_id, installation_id, type, delivery_id, payload, status,
    requirement_id, requirement_kind, occurrence_id, operation_id, recipient_user_id,
    requirement_revision, issue_revision, snapshot_payload, request_hash,
    attempt, next_attempt_at, not_sent, retryable, partial_delivery,
    evidence_finalized, needs_reconcile, dispatched_at,
    delivered_at, superseded_at, unknown_at, last_error, created_at, updated_at
)
SELECT
    outbox.workspace_id,
    cmd.bridge_id,
    cmd.installation_id,
    'send',
    outbox.delivery_id,
    COALESCE(cmd.payload, '{}'::jsonb),
    CASE outbox.status
        WHEN 'accepted' THEN 'pending'
        WHEN 'delivered' THEN 'delivered'
        WHEN 'failed' THEN 'failed'
        WHEN 'unknown' THEN 'unknown'
        WHEN 'superseded' THEN 'superseded'
        WHEN 'conflict' THEN 'conflict'
        ELSE 'pending'
    END,
    outbox.requirement_id,
    outbox.kind,
    outbox.occurrence_id,
    outbox.operation_id,
    outbox.recipient_user_id,
    outbox.requirement_revision,
    outbox.issue_revision,
    outbox.payload,
    outbox.request_hash,
    outbox.attempt,
    outbox.next_attempt_at,
    outbox.status NOT IN ('delivered', 'unknown'),
    false,
    false,
    outbox.status = 'delivered',
    outbox.needs_reconcile,
    CASE
        WHEN cmd.status IS NULL THEN NULL
        WHEN cmd.status = 'pending' THEN NULL
        ELSE cmd.updated_at
    END,
    outbox.delivered_at,
    outbox.superseded_at,
    outbox.unknown_at,
    outbox.last_error,
    outbox.created_at,
    outbox.updated_at
FROM requirement_outbox AS outbox
LEFT JOIN popo_bridge_command AS cmd ON cmd.id = outbox.transport_command_id
ON CONFLICT (delivery_id) DO UPDATE SET
    requirement_id = EXCLUDED.requirement_id,
    requirement_kind = EXCLUDED.requirement_kind,
    occurrence_id = EXCLUDED.occurrence_id,
    operation_id = EXCLUDED.operation_id,
    recipient_user_id = EXCLUDED.recipient_user_id,
    requirement_revision = EXCLUDED.requirement_revision,
    issue_revision = EXCLUDED.issue_revision,
    snapshot_payload = EXCLUDED.snapshot_payload,
    request_hash = EXCLUDED.request_hash,
    status = CASE
        WHEN popo_bridge_command.status IN ('delivered', 'unknown', 'superseded')
            THEN popo_bridge_command.status
        ELSE EXCLUDED.status
    END,
    lease_id = CASE
        WHEN popo_bridge_command.status IN ('delivered', 'unknown', 'superseded')
            THEN popo_bridge_command.lease_id
        ELSE NULL
    END,
    lease_expires_at = CASE
        WHEN popo_bridge_command.status IN ('delivered', 'unknown', 'superseded')
            THEN popo_bridge_command.lease_expires_at
        ELSE NULL
    END,
    not_sent = EXCLUDED.not_sent,
    evidence_finalized = EXCLUDED.evidence_finalized,
    needs_reconcile = EXCLUDED.needs_reconcile,
    dispatched_at = COALESCE(popo_bridge_command.dispatched_at, EXCLUDED.dispatched_at),
    next_attempt_at = EXCLUDED.next_attempt_at,
    delivered_at = COALESCE(popo_bridge_command.delivered_at, EXCLUDED.delivered_at),
    superseded_at = COALESCE(popo_bridge_command.superseded_at, EXCLUDED.superseded_at),
    unknown_at = COALESCE(popo_bridge_command.unknown_at, EXCLUDED.unknown_at),
    last_error = COALESCE(EXCLUDED.last_error, popo_bridge_command.last_error),
    attempt = GREATEST(popo_bridge_command.attempt, EXCLUDED.attempt),
    updated_at = now();
