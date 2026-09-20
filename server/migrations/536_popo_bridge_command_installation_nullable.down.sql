DELETE FROM popo_bridge_command WHERE installation_id IS NULL;
ALTER TABLE popo_bridge_command
    ALTER COLUMN installation_id SET NOT NULL;
