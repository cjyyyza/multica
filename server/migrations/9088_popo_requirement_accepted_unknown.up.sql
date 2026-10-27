-- 9085 mapped accepted outbox rows to pending. Accepted means the command may
-- already have reached the bridge, so those rows must not be leased again.
UPDATE popo_bridge_command
SET status = 'unknown',
    unknown_at = COALESCE(unknown_at, now()),
    not_sent = false,
    retryable = false,
    evidence_finalized = false,
    needs_reconcile = true,
    lease_id = NULL,
    lease_expires_at = NULL,
    last_error = 'POPO command was accepted before the single-ledger upgrade; receipt outcome is unknown',
    updated_at = now()
WHERE requirement_id IS NOT NULL
  AND status = 'pending'
  AND accepted_at IS NOT NULL
  AND dispatched_at IS NOT NULL;
