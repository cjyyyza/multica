-- register_qr / cancel_registration are not tied to an installation yet.
ALTER TABLE popo_bridge_command
    ALTER COLUMN installation_id DROP NOT NULL;
