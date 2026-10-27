UPDATE channel_user_binding AS binding
SET config = jsonb_set(binding.config, '{popo_default_mode}', '"requirement"', true)
FROM channel_installation AS installation
WHERE binding.installation_id = installation.id
  AND binding.channel_type = 'popo'
  AND installation.channel_type = 'popo'
  AND COALESCE(installation.config->>'requirement_template_url', '') <> ''
  AND NOT (binding.config ? 'popo_default_mode');
