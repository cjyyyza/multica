UPDATE channel_user_binding
SET config = config - 'popo_default_mode'
WHERE channel_type = 'popo'
  AND config ? 'popo_default_mode';
