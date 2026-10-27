ALTER TABLE popo_v2_delivery
    ADD COLUMN bridge_id UUID,
    ADD COLUMN installation_id UUID;

-- Route metadata is outside the canonical payload hash. Legacy rows with no
-- unambiguous active installation stay unrouted and must not be polled.
UPDATE popo_v2_delivery AS delivery
SET bridge_id = (installation.config->>'bridge_id')::uuid,
    installation_id = installation.id
FROM channel_installation AS installation
WHERE installation.workspace_id = delivery.workspace_id
  AND installation.channel_type = 'popo'
  AND installation.status = 'active'
  AND installation.config->>'app_id' = delivery.payload_snapshot->>'robot_id'
  AND installation.config->>'bridge_id' IS NOT NULL;
