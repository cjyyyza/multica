#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [ -f .env ]; then
  echo "Existing .env preserved."
  exit 0
fi
umask 077
jwt_secret=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
database_password=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
# Base64 of exactly 32 bytes: the at-rest key the Lark/Feishu integration
# requires. Generating it here turns the channel on for a fresh self-host
# deployment; each deployment gets its own key, never a shared one.
lark_key=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
sed -e "s|^JWT_SECRET=.*|JWT_SECRET=$jwt_secret|" \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$database_password|" \
    -e "s|^MULTICA_LARK_SECRET_KEY=.*|MULTICA_LARK_SECRET_KEY=$lark_key|" \
    .env.example > .env
echo "Created .env. Configure public URLs and email delivery before starting."
